package singleserver

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const cloudflareAPI = "https://api.cloudflare.com/client/v4"

type CloudflareState struct {
	APIToken        string `json:"api_token"`
	AccountID       string `json:"account_id"`
	TunnelID        string `json:"tunnel_id"`
	TunnelName      string `json:"tunnel_name"`
	TunnelSecret    string `json:"tunnel_secret"`
	HookHost        string `json:"hook_host"`
	CredentialsFile string `json:"credentials_file"`
	ConfigFile      string `json:"config_file"`
}

type CloudflareClient struct {
	token      string
	httpClient *http.Client
}

type cloudflareZone struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Account struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"account"`
}

type cloudflareTunnel struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Secret string `json:"tunnel_secret"`
}

type cloudflareDNSRecord struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
}

type cloudflaredConfig struct {
	Tunnel          string               `yaml:"tunnel"`
	CredentialsFile string               `yaml:"credentials-file"`
	Ingress         []cloudflaredIngress `yaml:"ingress"`
}

type cloudflaredIngress struct {
	Hostname string `yaml:"hostname,omitempty"`
	Service  string `yaml:"service"`
}

func newCloudflareClient(token string) (*CloudflareClient, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, errors.New("Cloudflare API token is required")
	}
	return &CloudflareClient{
		token:      token,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func loadCloudflareState() (*CloudflareState, error) {
	path := cloudflareStatePath()
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &CloudflareState{}, nil
		}
		return nil, err
	}
	var state CloudflareState
	if err := json.Unmarshal(body, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func writeCloudflareState(state *CloudflareState) error {
	body, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(cloudflareStatePath(), append(body, '\n'))
}

func cloudflareStatePath() string {
	return filepath.Join(envDefault("SINGLESERVER_STATE_DIR", "/etc/singleserver"), "cloudflare.json")
}

func cloudflareTokenFromEnvOrState(state *CloudflareState) string {
	if token := strings.TrimSpace(os.Getenv("CLOUDFLARE_API_TOKEN")); token != "" {
		return token
	}
	if token := strings.TrimSpace(os.Getenv("CF_API_TOKEN")); token != "" {
		return token
	}
	if state != nil {
		return strings.TrimSpace(state.APIToken)
	}
	return ""
}

func (c *CloudflareClient) zones(name string) ([]cloudflareZone, error) {
	path := "/zones"
	if strings.TrimSpace(name) != "" {
		path += "?name=" + urlQueryEscape(name)
	}
	var out struct {
		Result []cloudflareZone `json:"result"`
	}
	if err := c.request("GET", path, nil, &out); err != nil {
		return nil, err
	}
	return out.Result, nil
}

func (c *CloudflareClient) zoneForHostname(hostname string) (*cloudflareZone, error) {
	hostname = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(hostname)), ".")
	if hostname == "" {
		return nil, errors.New("hostname is required")
	}
	zones, err := c.zones("")
	if err != nil {
		return nil, err
	}
	return zoneForHostnameFromList(hostname, zones)
}

func zoneForHostnameFromList(hostname string, zones []cloudflareZone) (*cloudflareZone, error) {
	hostname = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(hostname)), ".")
	if hostname == "" {
		return nil, errors.New("hostname is required")
	}
	var best *cloudflareZone
	for i := range zones {
		zoneName := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(zones[i].Name)), ".")
		if zoneName == "" {
			continue
		}
		if hostname != zoneName && !strings.HasSuffix(hostname, "."+zoneName) {
			continue
		}
		if best == nil || len(zoneName) > len(best.Name) {
			best = &zones[i]
		}
	}
	if best == nil {
		return nil, fmt.Errorf("Cloudflare token cannot access a zone for %s", hostname)
	}
	return best, nil
}

func (c *CloudflareClient) findTunnel(accountID string, name string) (*cloudflareTunnel, error) {
	var out struct {
		Result []cloudflareTunnel `json:"result"`
	}
	path := fmt.Sprintf("/accounts/%s/cfd_tunnel?is_deleted=false&name=%s", accountID, urlQueryEscape(name))
	if err := c.request("GET", path, nil, &out); err != nil {
		return nil, err
	}
	for _, tunnel := range out.Result {
		if strings.EqualFold(tunnel.Name, name) {
			return &tunnel, nil
		}
	}
	return nil, nil
}

func (c *CloudflareClient) createTunnel(accountID string, name string, secret string) (*cloudflareTunnel, error) {
	var out struct {
		Result cloudflareTunnel `json:"result"`
	}
	body := map[string]string{
		"name":          name,
		"tunnel_secret": secret,
	}
	if err := c.request("POST", "/accounts/"+accountID+"/cfd_tunnel", body, &out); err != nil {
		return nil, err
	}
	if out.Result.ID == "" {
		return nil, errors.New("Cloudflare did not return a tunnel id")
	}
	out.Result.Secret = secret
	return &out.Result, nil
}

func (c *CloudflareClient) upsertCNAME(zoneID string, hostname string, target string) error {
	records, err := c.dnsRecords(zoneID, hostname, "CNAME")
	if err != nil {
		return err
	}
	if conflict := conflictingCNAMERecord(records, target); conflict != nil {
		return fmt.Errorf("Cloudflare DNS %s already points to %s; remove that CNAME before assigning it to Single Server", hostname, conflict.Content)
	}
	body := map[string]any{
		"type":    "CNAME",
		"name":    hostname,
		"content": target,
		"ttl":     1,
		"proxied": true,
	}
	if len(records) == 0 {
		return c.request("POST", "/zones/"+zoneID+"/dns_records", body, nil)
	}
	return c.request("PUT", "/zones/"+zoneID+"/dns_records/"+records[0].ID, body, nil)
}

func (c *CloudflareClient) deleteCNAMEToTarget(zoneID string, hostname string, target string) error {
	records, err := c.dnsRecords(zoneID, hostname, "CNAME")
	if err != nil {
		return err
	}
	for _, record := range records {
		if !dnsRecordContentMatches(record.Content, target) {
			continue
		}
		if err := c.request("DELETE", "/zones/"+zoneID+"/dns_records/"+record.ID, nil, nil); err != nil {
			return err
		}
	}
	return nil
}

func dnsRecordContentMatches(content string, target string) bool {
	content = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(content)), ".")
	target = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(target)), ".")
	return content != "" && content == target
}

func conflictingCNAMERecord(records []cloudflareDNSRecord, target string) *cloudflareDNSRecord {
	for i := range records {
		if dnsRecordContentMatches(records[i].Content, target) {
			continue
		}
		return &records[i]
	}
	return nil
}

func (c *CloudflareClient) dnsRecords(zoneID string, hostname string, recordType string) ([]cloudflareDNSRecord, error) {
	var out struct {
		Result []cloudflareDNSRecord `json:"result"`
	}
	path := fmt.Sprintf("/zones/%s/dns_records?type=%s&name=%s", zoneID, urlQueryEscape(recordType), urlQueryEscape(hostname))
	if err := c.request("GET", path, nil, &out); err != nil {
		return nil, err
	}
	return out.Result, nil
}

func (c *CloudflareClient) request(method string, path string, body any, output any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, cloudflareAPI+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "singleserver")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	resBody, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var apiError struct {
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		_ = json.Unmarshal(resBody, &apiError)
		message := strings.TrimSpace(string(resBody))
		if len(apiError.Errors) > 0 && apiError.Errors[0].Message != "" {
			message = apiError.Errors[0].Message
		}
		return fmt.Errorf("Cloudflare API %s %s failed: %s", method, path, message)
	}
	if output != nil && len(resBody) > 0 {
		var envelope struct {
			Success bool            `json:"success"`
			Result  json.RawMessage `json:"result"`
			Errors  []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		if err := json.Unmarshal(resBody, &envelope); err == nil && envelope.Result != nil {
			if !envelope.Success {
				if len(envelope.Errors) > 0 {
					return errors.New(envelope.Errors[0].Message)
				}
				return errors.New("Cloudflare API request failed")
			}
		}
		return json.Unmarshal(resBody, output)
	}
	return nil
}

func ensureCloudflaredRoute(configPath string, tunnelID string, credentialsFile string, hostname string, service string) error {
	config := cloudflaredConfig{
		Tunnel:          tunnelID,
		CredentialsFile: credentialsFile,
	}
	if body, err := os.ReadFile(configPath); err == nil && len(bytes.TrimSpace(body)) > 0 {
		_ = yaml.Unmarshal(body, &config)
	}
	config.Tunnel = tunnelID
	config.CredentialsFile = credentialsFile
	route := cloudflaredIngress{Hostname: hostname, Service: service}
	ingress := []cloudflaredIngress{}
	inserted := false
	for _, existing := range config.Ingress {
		if existing.Hostname == "" {
			if !inserted {
				ingress = append(ingress, route)
				inserted = true
			}
			ingress = append(ingress, existing)
			continue
		}
		if strings.EqualFold(existing.Hostname, hostname) {
			if !inserted {
				ingress = append(ingress, route)
				inserted = true
			}
			continue
		}
		ingress = append(ingress, existing)
	}
	if !inserted {
		ingress = append(ingress, route)
	}
	if len(ingress) == 0 || ingress[len(ingress)-1].Hostname != "" {
		ingress = append(ingress, cloudflaredIngress{Service: "http_status:404"})
	}
	config.Ingress = ingress

	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(config); err != nil {
		return err
	}
	if err := encoder.Close(); err != nil {
		return err
	}
	return writeFileAtomic(configPath, buf.Bytes())
}

func ensureCloudflaredConfig(configPath string, tunnelID string, credentialsFile string) error {
	config := cloudflaredConfig{
		Tunnel:          tunnelID,
		CredentialsFile: credentialsFile,
	}
	if body, err := os.ReadFile(configPath); err == nil && len(bytes.TrimSpace(body)) > 0 {
		_ = yaml.Unmarshal(body, &config)
	}
	config.Tunnel = tunnelID
	config.CredentialsFile = credentialsFile
	if len(config.Ingress) == 0 || config.Ingress[len(config.Ingress)-1].Hostname != "" {
		config.Ingress = append(config.Ingress, cloudflaredIngress{Service: "http_status:404"})
	}

	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(config); err != nil {
		return err
	}
	if err := encoder.Close(); err != nil {
		return err
	}
	return writeFileAtomic(configPath, buf.Bytes())
}

func removeCloudflaredRoute(configPath string, hostname string) error {
	body, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var config cloudflaredConfig
	if err := yaml.Unmarshal(body, &config); err != nil {
		return err
	}
	ingress := config.Ingress[:0]
	for _, existing := range config.Ingress {
		if !strings.EqualFold(existing.Hostname, hostname) {
			ingress = append(ingress, existing)
		}
	}
	config.Ingress = ingress
	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(config); err != nil {
		return err
	}
	if err := encoder.Close(); err != nil {
		return err
	}
	return writeFileAtomic(configPath, buf.Bytes())
}

func staleCloudflaredHosts(routes map[string]string, expected map[string]bool) []string {
	hosts := []string{}
	for host := range routes {
		if !expected[strings.ToLower(host)] {
			hosts = append(hosts, host)
		}
	}
	sort.Strings(hosts)
	return hosts
}

func pruneStaleCloudflareRoutes(client *CloudflareClient, state *CloudflareState, w io.Writer) error {
	if state == nil || state.ConfigFile == "" {
		return nil
	}
	config, err := LoadConfig(envDefault("SINGLESERVER_CONFIG", "/etc/singleserver/apps.yml"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	cloudflaredConfig, err := readCloudflaredConfig(state.ConfigFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	expected := expectedCloudflaredHosts(config.Apps)
	routes := cloudflaredRoutes(cloudflaredConfig)
	for _, host := range staleCloudflaredHosts(routes, expected) {
		if state.TunnelID != "" && client != nil {
			zone, err := client.zoneForHostname(host)
			if err != nil {
				return err
			}
			if err := client.deleteCNAMEToTarget(zone.ID, host, state.TunnelID+".cfargotunnel.com"); err != nil {
				return err
			}
		}
		if err := removeCloudflaredRoute(state.ConfigFile, host); err != nil {
			return err
		}
		writeCheck(w, "cloudflare", "route", "ok", host, "removed stale")
	}
	return nil
}

func randomTunnelSecret() (string, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(secret), nil
}

func urlQueryEscape(value string) string {
	return url.QueryEscape(value)
}

func writeCloudflaredCredentials(path string, state *CloudflareState) error {
	if state == nil || state.AccountID == "" || state.TunnelID == "" || state.TunnelSecret == "" {
		return errors.New("cloudflared credentials require account id, tunnel id, and tunnel secret")
	}
	body, err := json.MarshalIndent(map[string]string{
		"AccountTag":   state.AccountID,
		"TunnelSecret": state.TunnelSecret,
		"TunnelID":     state.TunnelID,
	}, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(body, '\n'))
}

func dnsLabelFromAppName(appName string) string {
	appName = strings.ToLower(strings.TrimSpace(appName))
	var builder strings.Builder
	lastHyphen := false
	for _, r := range appName {
		valid := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if valid {
			builder.WriteRune(r)
			lastHyphen = false
			continue
		}
		if builder.Len() == 0 || lastHyphen {
			continue
		}
		builder.WriteByte('-')
		lastHyphen = true
	}
	label := strings.Trim(builder.String(), "-")
	if len(label) > 63 {
		label = strings.TrimRight(label[:63], "-")
	}
	return label
}

var syncCloudflareAppDomainFunc = syncCloudflareAppDomain

type cloudflareDomainSyncOps struct {
	upsertRecord func(hostname string) error
	deleteRecord func(hostname string) error
	ensureRoute  func(hostname string) error
	removeRoute  func(hostname string) error
	restart      func() error
	target       string
	mode         string
}

func syncCloudflareAppDomain(hostname string, add bool, w io.Writer) error {
	state, err := loadCloudflareState()
	if err != nil {
		return err
	}
	if state.TunnelID == "" {
		writeCheck(w, "cloudflare", "setup", "skipped", "-", "connect Cloudflare first with `singleserver cloudflare connect`")
		return nil
	}
	client, err := newCloudflareClient(cloudflareTokenFromEnvOrState(state))
	if err != nil {
		return err
	}
	ops, err := cloudflareDomainOps(state, client)
	if err != nil {
		return err
	}
	return syncCloudflareAppDomainWithOps(hostname, add, w, state, ops)
}

func syncCloudflareAppDomainWithOps(hostname string, add bool, w io.Writer, state *CloudflareState, ops cloudflareDomainSyncOps) error {
	if add {
		if err := ops.ensureRoute(hostname); err != nil {
			return err
		}
		if err := ops.upsertRecord(hostname); err != nil {
			_ = ops.removeRoute(hostname)
			return err
		}
		if err := ops.restart(); err != nil {
			_ = ops.deleteRecord(hostname)
			_ = ops.removeRoute(hostname)
			return err
		}
		writeCheck(w, "cloudflare", "domain", "ok", hostname, "target="+ops.target)
	} else {
		if err := ops.removeRoute(hostname); err != nil {
			return err
		}
		if err := ops.deleteRecord(hostname); err != nil {
			_ = ops.ensureRoute(hostname)
			return err
		}
		if err := ops.restart(); err != nil {
			_ = ops.ensureRoute(hostname)
			_ = ops.upsertRecord(hostname)
			return err
		}
		writeCheck(w, "cloudflare", "domain", "ok", hostname, "removed")
	}
	return nil
}

func cloudflareDomainOps(state *CloudflareState, client *CloudflareClient) (cloudflareDomainSyncOps, error) {
	if state.TunnelID == "" || state.ConfigFile == "" {
		return cloudflareDomainSyncOps{}, errors.New("Cloudflare is connected but no tunnel route target is configured; run `singleserver cloudflare connect`")
	}
	target := state.TunnelID + ".cfargotunnel.com"
	return cloudflareDomainSyncOps{
		target: target,
		mode:   "tunnel",
		upsertRecord: func(hostname string) error {
			zone, err := client.zoneForHostname(hostname)
			if err != nil {
				return err
			}
			return client.upsertCNAME(zone.ID, hostname, target)
		},
		deleteRecord: func(hostname string) error {
			zone, err := client.zoneForHostname(hostname)
			if err != nil {
				return err
			}
			return client.deleteCNAMEToTarget(zone.ID, hostname, target)
		},
		ensureRoute: func(hostname string) error {
			return ensureCloudflaredRoute(state.ConfigFile, state.TunnelID, state.CredentialsFile, hostname, "http://127.0.0.1:80")
		},
		removeRoute: func(hostname string) error {
			return removeCloudflaredRoute(state.ConfigFile, hostname)
		},
		restart: func() error {
			return commandRunFunc(10*time.Second, "systemctl", "restart", "cloudflared-singleserver.service")
		},
	}, nil
}
