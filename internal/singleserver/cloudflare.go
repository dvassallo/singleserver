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
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const cloudflareAPI = "https://api.cloudflare.com/client/v4"

type CloudflareState struct {
	APIToken        string `json:"api_token"`
	AccountID       string `json:"account_id"`
	ZoneID          string `json:"zone_id"`
	ZoneName        string `json:"zone_name"`
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
	Proxied bool   `json:"proxied"`
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

func (c *CloudflareClient) upsertCNAME(zoneID string, hostname string, target string, proxied bool) error {
	records, err := c.dnsRecords(zoneID, hostname, "CNAME")
	if err != nil {
		return err
	}
	body := map[string]any{
		"type":    "CNAME",
		"name":    hostname,
		"content": target,
		"ttl":     1,
		"proxied": proxied,
	}
	if len(records) == 0 {
		return c.request("POST", "/zones/"+zoneID+"/dns_records", body, nil)
	}
	return c.request("PUT", "/zones/"+zoneID+"/dns_records/"+records[0].ID, body, nil)
}

func (c *CloudflareClient) deleteDNSRecord(zoneID string, hostname string, recordType string) error {
	records, err := c.dnsRecords(zoneID, hostname, recordType)
	if err != nil {
		return err
	}
	for _, record := range records {
		if err := c.request("DELETE", "/zones/"+zoneID+"/dns_records/"+record.ID, nil, nil); err != nil {
			return err
		}
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

func defaultAppDomain(appName string) (string, bool, error) {
	state, err := loadCloudflareState()
	if err != nil {
		return "", false, err
	}
	if state.ZoneName == "" {
		return "", false, nil
	}
	return appName + "." + state.ZoneName, true, nil
}

func syncCloudflareAppDomain(hostname string, add bool, w io.Writer) error {
	state, err := loadCloudflareState()
	if err != nil {
		return err
	}
	if state.ZoneID == "" || state.TunnelID == "" || state.ConfigFile == "" {
		fmt.Fprintf(w, "cloudflare\tskipped\tconnect Cloudflare first with `singleserver cloudflare connect`\n")
		return nil
	}
	client, err := newCloudflareClient(cloudflareTokenFromEnvOrState(state))
	if err != nil {
		fmt.Fprintf(w, "cloudflare\tskipped\t%s\n", err)
		return nil
	}
	if add {
		if err := client.upsertCNAME(state.ZoneID, hostname, state.TunnelID+".cfargotunnel.com", true); err != nil {
			return err
		}
		if err := ensureCloudflaredRoute(state.ConfigFile, state.TunnelID, state.CredentialsFile, hostname, "http://127.0.0.1:80"); err != nil {
			return err
		}
		fmt.Fprintf(w, "cloudflare\tdomain\tok\t%s -> %s.cfargotunnel.com\n", hostname, state.TunnelID)
	} else {
		if err := client.deleteDNSRecord(state.ZoneID, hostname, "CNAME"); err != nil {
			return err
		}
		if err := removeCloudflaredRoute(state.ConfigFile, hostname); err != nil {
			return err
		}
		fmt.Fprintf(w, "cloudflare\tdomain\tok\tremoved %s\n", hostname)
	}
	_ = commandRun(10*time.Second, "systemctl", "restart", "cloudflared-singleserver.service")
	return nil
}
