package singleserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Funnel publishes nodes, not Services, so a funnelled app gets its own tailscale container as a node.

const (
	funnelAccessory   = "funnel"
	funnelImage       = "tailscale/tailscale:stable"
	funnelProxyTarget = "http://kamal-proxy:80"
	funnelRevisionEnv = "SINGLESERVER_FUNNEL_REV"
)

func funnelRoot() string {
	return envDefault("SINGLESERVER_FUNNEL_ROOT", "/var/lib/singleserver/funnel")
}

func funnelDir(app AppConfig) string { return filepath.Join(funnelRoot(), app.Name) }

func funnelStateDir(app AppConfig) string { return filepath.Join(funnelDir(app), "state") }

func funnelConfigDir(app AppConfig) string { return filepath.Join(funnelDir(app), "config") }

func funnelContainerName(app AppConfig) string { return app.Name + "-" + funnelAccessory }

type tailscaleServeConfig struct {
	TCP         map[string]tailscaleServeTCP `json:"TCP"`
	Web         map[string]tailscaleServeWeb `json:"Web"`
	AllowFunnel map[string]bool              `json:"AllowFunnel"`
}

type tailscaleServeTCP struct {
	HTTPS bool `json:"HTTPS"`
}

type tailscaleServeWeb struct {
	Handlers map[string]tailscaleServeHandler `json:"Handlers"`
}

type tailscaleServeHandler struct {
	Proxy string `json:"Proxy"`
}

// ipn.ServeConfig for containerboot, which substitutes ${TS_CERT_DOMAIN} with the node's MagicDNS name.
func funnelServeConfig(app AppConfig) ([]byte, error) {
	if !app.HasFunnel() {
		return nil, fmt.Errorf("%s has no funnel", app.Name)
	}
	handlers := map[string]tailscaleServeHandler{}
	for _, p := range app.Funnel.Paths {
		target := funnelProxyTarget
		// Serve strips the mount point before proxying; the target path puts it back.
		if p != "/" {
			target += p
		}
		handlers[p] = tailscaleServeHandler{Proxy: target}
	}
	hostPort := "${TS_CERT_DOMAIN}:443"
	config := tailscaleServeConfig{
		TCP:         map[string]tailscaleServeTCP{"443": {HTTPS: true}},
		Web:         map[string]tailscaleServeWeb{hostPort: {Handlers: handlers}},
		AllowFunnel: map[string]bool{hostPort: true},
	}
	body, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(body, '\n'), nil
}

// funnelRevision changes whenever the container must be recreated to pick up new settings.
func funnelRevision(app AppConfig) (string, error) {
	serve, err := funnelServeConfig(app)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{funnelImage, app.FunnelLabel(), tailscaleServiceTag, string(serve)}, "\n")))
	return hex.EncodeToString(sum[:])[:12], nil
}

type kamalAccessory struct {
	Image   string            `yaml:"image"`
	Host    string            `yaml:"host"`
	Env     kamalAccessoryEnv `yaml:"env"`
	Volumes []string          `yaml:"volumes"`
}

type kamalAccessoryEnv struct {
	Clear  map[string]string `yaml:"clear"`
	Secret []string          `yaml:"secret"`
}

func kamalFunnelAccessory(app AppConfig) (kamalAccessory, error) {
	revision, err := funnelRevision(app)
	if err != nil {
		return kamalAccessory{}, err
	}
	return kamalAccessory{
		Image: funnelImage,
		Host:  "127.0.0.1",
		Env: kamalAccessoryEnv{
			Clear: map[string]string{
				"TS_HOSTNAME":     app.FunnelLabel(),
				"TS_USERSPACE":    "true",
				"TS_STATE_DIR":    "/var/lib/tailscale",
				"TS_SERVE_CONFIG": "/config/serve.json",
				"TS_AUTH_ONCE":    "true",
				"TS_EXTRA_ARGS":   "--advertise-tags=" + tailscaleServiceTag,
				funnelRevisionEnv: revision,
			},
			Secret: []string{"TS_AUTHKEY"},
		},
		Volumes: []string{
			funnelStateDir(app) + ":/var/lib/tailscale",
			funnelConfigDir(app) + ":/config:ro",
		},
	}, nil
}

func writeFunnelConfig(app AppConfig) error {
	serve, err := funnelServeConfig(app)
	if err != nil {
		return err
	}
	for _, dir := range []string{funnelStateDir(app), funnelConfigDir(app)} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
	}
	return writeFileAtomic(filepath.Join(funnelConfigDir(app), "serve.json"), serve)
}

type funnelContainerState struct {
	exists   bool
	running  bool
	revision string
	hostname string
	backend  string
}

var (
	funnelContainerStateFunc = inspectFunnelContainer
	funnelStatusRetryDelay   = 2 * time.Second
)

func inspectFunnelContainer(app AppConfig) funnelContainerState {
	name := funnelContainerName(app)
	out, err := commandOutputFunc(5*time.Second, "docker", "inspect", "--format", "{{.State.Running}}{{range .Config.Env}} {{.}}{{end}}", name)
	if err != nil {
		return funnelContainerState{}
	}
	state := funnelContainerState{exists: true}
	for i, field := range strings.Fields(out) {
		if i == 0 {
			state.running = field == "true"
			continue
		}
		if value, ok := strings.CutPrefix(field, funnelRevisionEnv+"="); ok {
			state.revision = value
		}
		if value, ok := strings.CutPrefix(field, "TS_HOSTNAME="); ok {
			state.hostname = value
		}
	}
	if !state.running {
		return state
	}
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(funnelStatusRetryDelay)
		}
		status, err := commandOutputFunc(10*time.Second, "docker", "exec", name, "tailscale", "status", "--json")
		if err != nil {
			continue
		}
		var parsed tailscaleStatus
		if json.Unmarshal([]byte(status), &parsed) == nil && parsed.BackendState != "" {
			state.backend = parsed.BackendState
			break
		}
	}
	return state
}

// The reason is empty when the running container matches the config; a key is only needed when the node must log in.
func funnelBootPlan(app AppConfig) (reason string, needsKey bool, err error) {
	revision, err := funnelRevision(app)
	if err != nil {
		return "", false, err
	}
	state := funnelContainerStateFunc(app)
	switch {
	case !state.exists:
		return "first boot", true, nil
	case state.revision != revision:
		return "config changed", false, nil
	case !state.running:
		return "container stopped", false, nil
	case state.backend == "":
		return "tailscale status unavailable", false, nil
	case !strings.EqualFold(state.backend, "Running"):
		return "tailscale " + strings.ToLower(state.backend), true, nil
	}
	return "", false, nil
}

type tailscaleAuthKeyRequest struct {
	Capabilities  tailscaleAuthKeyCapabilities `json:"capabilities"`
	ExpirySeconds int                          `json:"expirySeconds"`
	Description   string                       `json:"description"`
}

type tailscaleAuthKeyCapabilities struct {
	Devices struct {
		Create struct {
			Reusable      bool     `json:"reusable"`
			Ephemeral     bool     `json:"ephemeral"`
			Preauthorized bool     `json:"preauthorized"`
			Tags          []string `json:"tags"`
		} `json:"create"`
	} `json:"devices"`
}

var mintFunnelAuthKeyFunc = mintFunnelAuthKey

// A single-use, tagged key that only the node's first login consumes.
func mintFunnelAuthKey(token string, app AppConfig) (string, error) {
	var payload tailscaleAuthKeyRequest
	payload.Capabilities.Devices.Create.Preauthorized = true
	payload.Capabilities.Devices.Create.Tags = []string{tailscaleServiceTag}
	payload.ExpirySeconds = 3600
	payload.Description = "Single Server funnel for " + app.Name
	status, body, err := tailscaleAPIRequest(token, http.MethodPost, "/api/v2/tailnet/-/keys", payload)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return "", fmt.Errorf("creating a tailscale auth key returned HTTP %d: %s; the stored OAuth client needs the Auth Keys write scope tagged %s", status, strings.TrimSpace(string(body)), tailscaleServiceTag)
	}
	var parsed struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", err
	}
	if parsed.Key == "" {
		return "", errors.New("tailscale auth key response had no key")
	}
	return parsed.Key, nil
}

// Writes the serve config and tells the deploy script whether to (re)boot the container.
func prepareFunnelDeploy(app AppConfig) ([]string, error) {
	if !app.HasFunnel() {
		return nil, nil
	}
	if err := writeFunnelConfig(app); err != nil {
		return nil, err
	}
	reason, needsKey, err := funnelBootPlan(app)
	if err != nil {
		return nil, err
	}
	if reason == "" {
		return []string{"SINGLESERVER_FUNNEL_BOOT=0"}, nil
	}
	env := []string{"SINGLESERVER_FUNNEL_BOOT=1", "SINGLESERVER_FUNNEL_REASON=" + reason}
	if !needsKey {
		return env, nil
	}
	state, err := loadTailscaleState()
	if err != nil {
		return nil, err
	}
	clientID, clientSecret := tailscaleOAuthCredentials(state)
	if clientID == "" || clientSecret == "" {
		return nil, errTailscaleOAuthMissing
	}
	token, err := tailscaleAPIToken(clientID, clientSecret)
	if err != nil {
		return nil, fmt.Errorf("tailscale API authentication failed (check the stored OAuth client): %w", err)
	}
	key, err := mintFunnelAuthKeyFunc(token, app)
	if err != nil {
		return nil, err
	}
	return append(env, "SINGLESERVER_FUNNEL_AUTHKEY="+key), nil
}

var teardownFunnelFunc = teardownFunnel

// Logs the node out and removes the container and its state; Tailscale lists the machine until an admin deletes it.
func teardownFunnel(app AppConfig, w io.Writer) error {
	name := funnelContainerName(app)
	state := funnelContainerStateFunc(app)
	if !state.exists {
		return os.RemoveAll(funnelDir(app))
	}
	if state.running {
		_ = commandRunFunc(30*time.Second, "docker", "exec", name, "tailscale", "logout")
	}
	if err := commandRunFunc(30*time.Second, "docker", "rm", "-f", name); err != nil {
		return fmt.Errorf("removing %s: %w", name, err)
	}
	if err := os.RemoveAll(funnelDir(app)); err != nil {
		return err
	}
	machine := state.hostname
	if machine == "" {
		machine = app.FunnelLabel()
	}
	writeCheck(w, app.Name, "funnel", "ok", "removed", "delete the machine "+valueOrDash(machine)+" in the Tailscale admin console to forget it entirely")
	return nil
}

var (
	funnelReadyFunc      = waitForFunnelReady
	funnelLookupHostFunc = func(ctx context.Context, host string) ([]string, error) {
		return publicResolver().LookupHost(ctx, host)
	}
)

// Any HTTP answer counts; what matters is getting one through the public Funnel edge, not MagicDNS.
func waitForFunnelReady(rawURL string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := healthcheckClient()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			cancel()
			return err
		}
		res, err := client.Do(req)
		cancel()
		if err == nil {
			_, _ = io.Copy(io.Discard, res.Body)
			_ = res.Body.Close()
			return nil
		}
		lastErr = err
		time.Sleep(3 * time.Second)
	}
	return fmt.Errorf("funnel did not answer at %s: %w", rawURL, lastErr)
}

func doctorFunnel(w io.Writer, app AppConfig) bool {
	if !app.HasFunnel() {
		return true
	}
	ok := true
	state := funnelContainerStateFunc(app)
	name := funnelContainerName(app)
	switch {
	case !state.exists:
		writeCheck(w, app.Name, "funnel_container", "failed", name, "missing; deploy the app to boot it")
		ok = false
	case !state.running:
		writeCheck(w, app.Name, "funnel_container", "failed", name, "stopped; deploy the app to reboot it")
		ok = false
	case !strings.EqualFold(state.backend, "Running"):
		writeCheck(w, app.Name, "funnel_container", "failed", name, "tailscale "+valueOrDash(strings.ToLower(state.backend)), "deploy the app to re-authenticate the node")
		ok = false
	default:
		writeCheck(w, app.Name, "funnel_container", "ok", name, "node="+app.FunnelLabel())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	addrs, err := funnelLookupHostFunc(ctx, app.FunnelHost())
	cancel()
	if err != nil || len(addrs) == 0 {
		writeCheck(w, app.Name, "funnel_dns", "failed", app.FunnelHost(), "not in public DNS; Funnel is off for this node or still propagating")
		ok = false
	} else {
		writeCheck(w, app.Name, "funnel_dns", "ok", app.FunnelHost(), "public")
	}
	if err := funnelReadyFunc(app.FunnelURL(), 5*time.Second); err != nil {
		writeCheck(w, app.Name, "funnel_https", "failed", app.FunnelURL(), err.Error(),
			"Funnel needs the funnel node attribute for "+tailscaleServiceTag+" in the tailnet policy, and new nodes can take a minute to get a certificate")
		return false
	}
	writeCheck(w, app.Name, "funnel_https", "ok", app.FunnelURL(), "paths="+strings.Join(app.Funnel.Paths, ","))
	return ok
}
