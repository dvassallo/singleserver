package singleserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Private apps are served as Tailscale Services, one VIP service per app.

var tailscaleAPIBaseURL = "https://api.tailscale.com"

type vipService struct {
	Name    string   `json:"name,omitempty"`
	Addrs   []string `json:"addrs,omitempty"`
	Comment string   `json:"comment,omitempty"`
	Ports   []string `json:"ports,omitempty"`
	Tags    []string `json:"tags,omitempty"`
}

func tailscaleOAuthCredentials(state *TailscaleState) (string, string) {
	id := strings.TrimSpace(os.Getenv("TAILSCALE_OAUTH_CLIENT_ID"))
	secret := strings.TrimSpace(os.Getenv("TAILSCALE_OAUTH_CLIENT_SECRET"))
	if id != "" && secret != "" {
		return id, secret
	}
	if state != nil {
		return state.OAuthClientID, state.OAuthClientSecret
	}
	return "", ""
}

func tailscaleAPIToken(clientID, clientSecret string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	form := url.Values{"client_id": {clientID}, "client_secret": {clientSecret}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tailscaleAPIBaseURL+"/api/v2/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("tailscale oauth token request returned %s: %s", res.Status, strings.TrimSpace(string(body)))
	}
	var parsed struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", err
	}
	if parsed.AccessToken == "" {
		return "", errors.New("tailscale oauth token response had no access_token")
	}
	return parsed.AccessToken, nil
}

func tailscaleAPIRequest(token, method, path string, payload any) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var reqBody io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, err
		}
		reqBody = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, tailscaleAPIBaseURL+path, reqBody)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	return res.StatusCode, body, nil
}

func vipServicePath(name string) string {
	return "/api/v2/tailnet/-/vip-services/" + url.PathEscape("svc:"+name)
}

var ensureVIPServiceFunc = ensureVIPService

func ensureVIPService(token, name, comment string) (bool, error) {
	svc := vipService{
		Name:    "svc:" + name,
		Comment: comment,
		Ports:   []string{"tcp:443"},
		Tags:    []string{tailscaleServiceTag},
	}
	created := true
	status, body, err := tailscaleAPIRequest(token, http.MethodGet, vipServicePath(name), nil)
	if err != nil {
		return false, err
	}
	switch status {
	case http.StatusOK:
		var existing vipService
		if err := json.Unmarshal(body, &existing); err != nil {
			return false, fmt.Errorf("reading tailscale service svc:%s: %w", name, err)
		}
		if !containsFold(existing.Tags, tailscaleServiceTag) {
			return false, fmt.Errorf("svc:%s already exists in the tailnet and was not created by Single Server; rerun with a --domain whose first label is unique, or delete the service in the admin console", name)
		}
		svc.Addrs = existing.Addrs
		created = false
	case http.StatusNotFound:
	default:
		return false, fmt.Errorf("checking tailscale service svc:%s returned HTTP %d: %s", name, status, strings.TrimSpace(string(body)))
	}
	status, body, err = tailscaleAPIRequest(token, http.MethodPut, vipServicePath(name), svc)
	if err != nil {
		return false, err
	}
	if status != http.StatusOK && status != http.StatusCreated {
		if strings.Contains(string(body), "not a service") {
			return false, fmt.Errorf("the tailnet already has a machine named %q, which blocks the service's DNS name; remove that machine in the admin console (Machines) and retry: HTTP %d: %s", name, status, strings.TrimSpace(string(body)))
		}
		return false, fmt.Errorf("creating tailscale service svc:%s returned HTTP %d: %s", name, status, strings.TrimSpace(string(body)))
	}
	return created, nil
}

var deleteVIPServiceFunc = deleteVIPService

func deleteVIPService(token, name string) error {
	status, body, err := tailscaleAPIRequest(token, http.MethodDelete, vipServicePath(name), nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK && status != http.StatusNoContent && status != http.StatusNotFound {
		return fmt.Errorf("deleting tailscale service svc:%s returned HTTP %d: %s", name, status, strings.TrimSpace(string(body)))
	}
	return nil
}

const tailscaleServiceTag = "tag:singleserver"

func promptTailscaleOAuthClient(w io.Writer) (string, string, error) {
	if !cliCanPrompt(currentCLIMode()) {
		return "", "", nil
	}
	fmt.Fprintln(w, "Private apps are served as Tailscale Services, managed through the Tailscale API.")
	fmt.Fprintln(w, "One tag, "+tailscaleServiceTag+", marks both this server and its app services.")
	fmt.Fprintln(w, "In the admin console (https://login.tailscale.com/admin), once per tailnet:")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "1. Access controls -> JSON editor: add two top-level entries to the policy file:")
	fmt.Fprintf(w, "     \"tagOwners\": {\"%s\": [\"autogroup:admin\"]}\n", tailscaleServiceTag)
	fmt.Fprintf(w, "     \"autoApprovers\": {\"services\": {\"%s\": [\"%s\"]}}\n", tailscaleServiceTag, tailscaleServiceTag)
	fmt.Fprintln(w, "   The second entry lets this server host new app services without per-app approval.")
	fmt.Fprintln(w, "2. Machines -> this server -> Edit ACL tags: add "+tailscaleServiceTag+".")
	fmt.Fprintln(w, "3. Settings -> Trust credentials: create an OAuth client with the Services")
	fmt.Fprintln(w, "   write scope, tagged "+tailscaleServiceTag+". It never expires.")
	fmt.Fprintln(w, "   To publish paths through Tailscale Funnel (--funnel-path), also give it the")
	fmt.Fprintln(w, "   Auth Keys write scope with the same tag, and add to the policy file:")
	fmt.Fprintf(w, "     \"nodeAttrs\": [{\"target\": [\"%s\"], \"attr\": [\"funnel\"]}]\n", tailscaleServiceTag)
	p := interactivePrompter(w)
	for {
		id, err := p.askOptional("OAuth client ID (empty to skip)")
		if err != nil {
			return "", "", err
		}
		id = strings.TrimSpace(id)
		if id == "" {
			return "", "", nil
		}
		secret, err := p.askOptional("OAuth client secret")
		if err != nil {
			return "", "", err
		}
		secret = strings.TrimSpace(secret)
		if secret == "" {
			fmt.Fprintln(w, "The client secret is required; it is shown once when the client is created.")
			continue
		}
		if _, err := tailscaleAPIToken(id, secret); err != nil {
			fmt.Fprintf(w, "Those credentials didn't work: %v\n", err)
			continue
		}
		return id, secret, nil
	}
}

func tailscaleServiceNameForHost(hostname, appName string) string {
	hostname = strings.ToLower(strings.TrimSpace(hostname))
	if hostname == "" {
		return strings.ToLower(appName)
	}
	if label, _, ok := strings.Cut(hostname, "."); ok && label != "" {
		return label
	}
	return hostname
}

func tailscaleServiceName(app AppConfig) string {
	host := ""
	if len(app.Hosts) > 0 {
		host = app.Hosts[0]
	}
	return tailscaleServiceNameForHost(host, app.Name)
}

var errTailscaleOAuthMissing = errors.New("a Tailscale OAuth client (scopes: services write, plus auth keys write for funnel paths) is required for private apps; run `singleserver connect tailscale --oauth-client-id <id> --oauth-client-secret <secret>` or set TAILSCALE_OAUTH_CLIENT_ID and TAILSCALE_OAUTH_CLIENT_SECRET")

func storeTailscaleOAuthClient(state *TailscaleState, id, secret string, w io.Writer) error {
	state.OAuthClientID = id
	state.OAuthClientSecret = secret
	if err := writeTailscaleState(state); err != nil {
		return err
	}
	writeCheck(w, "tailscale", "oauth", "ok", "stored for private app services")
	return nil
}

// Prompting stays at command level: syncTailscaleAppDomain must never block on stdin.
func ensureTailscaleServicesReady(w io.Writer) error {
	state, err := loadTailscaleState()
	if err != nil {
		return err
	}
	if id, secret := tailscaleOAuthCredentials(state); id != "" && secret != "" {
		return nil
	}
	id, secret, err := promptTailscaleOAuthClient(w)
	if err != nil {
		return err
	}
	if id == "" || secret == "" {
		return errTailscaleOAuthMissing
	}
	return storeTailscaleOAuthClient(state, id, secret, w)
}

func syncTailscaleAppDomain(app AppConfig, hostname string, add bool, w io.Writer) error {
	hostname = app.QualifiedHost(hostname)
	serviceName := tailscaleServiceNameForHost(hostname, app.Name)

	state, err := loadTailscaleState()
	if err != nil {
		return err
	}
	clientID, clientSecret := tailscaleOAuthCredentials(state)
	if clientID == "" || clientSecret == "" {
		if !add {
			writeCheck(w, app.Name, "tailscale_service", "pending", "no OAuth client stored; delete svc:"+serviceName+" in the admin console")
			return unserveTailscaleService(app.Name, serviceName, w)
		}
		return errTailscaleOAuthMissing
	}

	if add {
		if !strings.Contains(hostname, ".") {
			return fmt.Errorf("cannot qualify %s: this server's tailnet is unknown; run `singleserver connect tailscale` first", hostname)
		}
		if domain := tailnetDomainFromHostname(state.Hostname); domain != "" && !strings.HasSuffix(strings.ToLower(hostname), "."+strings.ToLower(domain)) {
			return fmt.Errorf("%s is not on this server's tailnet; the app is served at %s.%s, so use that domain (or none)", hostname, serviceName, domain)
		}

		if status, err := currentTailscaleStatus(); err == nil && status.Self != nil && len(status.Self.Tags) == 0 {
			return errors.New("this server's Tailscale node has no tags, and only tagged nodes can host Tailscale Services; add " + tailscaleServiceTag + " to this machine in the admin console (Machines -> Edit ACL tags)")
		}
	}

	token, err := tailscaleAPIToken(clientID, clientSecret)
	if err != nil {
		return fmt.Errorf("tailscale API authentication failed (check the stored OAuth client): %w", err)
	}

	if add {
		writeCheck(w, app.Name, "tailscale_service", "creating", "svc:"+serviceName)
		created, err := ensureVIPServiceFunc(token, serviceName, "Single Server app "+app.Name)
		if err != nil {
			return err
		}

		writeCheck(w, app.Name, "tailscale_serve", "configuring", "svc:"+serviceName+" -> 127.0.0.1:80")
		serveArgs := []string{"serve", "--service=svc:" + serviceName, "--https=443", "http://127.0.0.1:80"}
		if err := commandRunFunc(30*time.Second, "tailscale", serveArgs...); err != nil {
			if created {
				_ = deleteVIPServiceFunc(token, serviceName)
			}
			return fmt.Errorf("failed to serve svc:%s: %w", serviceName, err)
		}

		writeCheck(w, app.Name, "tailscale_tunnel", "ok", hostname,
			"if the app stays unreachable, approve this host on the admin console's Services page or add the autoApprovers policy entry")
	} else {
		if err := unserveTailscaleService(app.Name, serviceName, w); err != nil {
			return err
		}
		if err := deleteVIPServiceFunc(token, serviceName); err != nil {
			return err
		}
		writeCheck(w, app.Name, "tailscale_tunnel", "ok", "removed")
	}

	return nil
}

func unserveTailscaleService(appName, serviceName string, w io.Writer) error {
	writeCheck(w, appName, "tailscale_serve", "stopping", "svc:"+serviceName)
	// Drain is best-effort: older clients lack it.
	_ = commandRunFunc(30*time.Second, "tailscale", "serve", "drain", "svc:"+serviceName)
	if err := commandRunFunc(30*time.Second, "tailscale", "serve", "--service=svc:"+serviceName, "--https=443", "off"); err != nil {
		return fmt.Errorf("failed to stop serving svc:%s; this host may still serve it, run `tailscale serve --service=svc:%s --https=443 off`: %w", serviceName, serviceName, err)
	}
	return nil
}

func doctorTailscaleServices(w io.Writer) {
	state, err := loadTailscaleState()
	if err != nil {
		return
	}
	clientID, clientSecret := tailscaleOAuthCredentials(state)
	if clientID == "" || clientSecret == "" {
		return
	}
	if _, err := tailscaleAPIToken(clientID, clientSecret); err != nil {
		writeCheck(w, "tailscale", "oauth", "failed", err.Error(),
			"private app services cannot be managed; store a working OAuth client with `singleserver connect tailscale --oauth-client-id <id> --oauth-client-secret <secret>`")
		return
	}
	writeCheck(w, "tailscale", "oauth", "ok", "services API reachable")
}
