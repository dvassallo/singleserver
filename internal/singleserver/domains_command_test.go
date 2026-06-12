package singleserver

import (
	"bytes"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDomainsRemoveSupportsNoDeployFlagAfterDomain(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps.yml")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	if err := os.WriteFile(configPath, []byte(`apps:
  - repo: dvassallo/fullsend
    hosts:
      - play.nobrainer.host
    healthcheck: https://play.nobrainer.host/up
`), 0600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	logger := log.New(io.Discard, "", 0)
	if err := cliDomains([]string{"remove", "fullsend", "play.nobrainer.host", "--no-deploy"}, &out, logger); err != nil {
		t.Fatal(err)
	}

	config, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Apps[0].Hosts) != 0 {
		t.Fatalf("expected host removed, got %#v", config.Apps[0].Hosts)
	}
	if config.Apps[0].Healthcheck != "" {
		t.Fatalf("expected removed default healthcheck to be cleared, got %s", config.Apps[0].Healthcheck)
	}
}

func TestDomainsAddRejectsHostUsedByAnotherApp(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps.yml")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	if err := os.WriteFile(configPath, []byte(`apps:
  - repo: dvassallo/fullsend
    hosts:
      - play.nobrainer.host
  - repo: dvassallo/sillyface-games
`), 0600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	logger := log.New(io.Discard, "", 0)
	err := cliDomains([]string{"add", "sillyface-games", "play.nobrainer.host", "--no-deploy"}, &out, logger)
	if err == nil {
		t.Fatal("expected duplicate host error")
	}
	if !strings.Contains(err.Error(), "duplicate host in config") {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(out.String(), "domain\tok") {
		t.Fatalf("unexpected success output: %s", out.String())
	}
}

func TestDomainsAddKeepsConfigWhenCloudflareFails(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps.yml")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	if err := os.WriteFile(configPath, []byte("apps:\n  - dvassallo/fullsend\n"), 0600); err != nil {
		t.Fatal(err)
	}

	originalSync := syncCloudflareAppDomainFunc
	t.Cleanup(func() { syncCloudflareAppDomainFunc = originalSync })
	syncCloudflareAppDomainFunc = func(hostname string, add bool, w io.Writer) error {
		return errors.New("cloudflare unavailable")
	}

	var out bytes.Buffer
	logger := log.New(io.Discard, "", 0)
	err := cliDomains([]string{"add", "fullsend", "play.nobrainer.host", "--no-deploy"}, &out, logger)
	if err == nil {
		t.Fatal("expected Cloudflare error")
	}
	if !strings.Contains(err.Error(), "cloudflare unavailable") {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(out.String(), "domain\tok") {
		t.Fatalf("unexpected success output: %s", out.String())
	}

	config, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Apps[0].Hosts) != 0 {
		t.Fatalf("expected config unchanged, got hosts %#v", config.Apps[0].Hosts)
	}
	if config.Apps[0].Healthcheck != "" {
		t.Fatalf("expected healthcheck unchanged, got %s", config.Apps[0].Healthcheck)
	}
}

func TestDomainsRemoveRejectsHostNotConfiguredForApp(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps.yml")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	if err := os.WriteFile(configPath, []byte(`apps:
  - repo: dvassallo/fullsend
    hosts:
      - play.nobrainer.host
`), 0600); err != nil {
		t.Fatal(err)
	}

	originalSync := syncCloudflareAppDomainFunc
	t.Cleanup(func() { syncCloudflareAppDomainFunc = originalSync })
	syncCalled := false
	syncCloudflareAppDomainFunc = func(hostname string, add bool, w io.Writer) error {
		syncCalled = true
		return nil
	}

	var out bytes.Buffer
	logger := log.New(io.Discard, "", 0)
	err := cliDomains([]string{"remove", "fullsend", "other.nobrainer.host", "--no-deploy"}, &out, logger)
	if err == nil {
		t.Fatal("expected unowned host error")
	}
	if !strings.Contains(err.Error(), "other.nobrainer.host is not configured for fullsend") {
		t.Fatalf("unexpected error: %v", err)
	}
	if syncCalled {
		t.Fatal("did not expect Cloudflare sync for unowned host")
	}
}

func TestDomainsVerifyDoesNotRequireTunnelRoute(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps.yml")
	tunnelConfigPath := filepath.Join(dir, "cloudflared.yml")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	t.Setenv("SINGLESERVER_STATE_DIR", dir)
	stubCommandRun(t)
	if err := os.WriteFile(configPath, []byte(`apps:
  - repo: dvassallo/fullsend
    hosts:
      - localhost
`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cloudflare.json"), []byte(`{"tunnel_id":"tunnel","config_file":"`+tunnelConfigPath+`"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tunnelConfigPath, []byte(`ingress:
  - hostname: localhost
    service: http://127.0.0.1:80
  - service: http_status:404
`), 0600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := cliDomains([]string{"verify", "fullsend"}, &out, log.New(io.Discard, "", 0)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "tunnel_route") {
		t.Fatalf("domains verify should not inspect tunnel routes, got:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "fullsend\tdns\tok\tlocalhost") {
		t.Fatalf("expected resolver DNS ok output, got:\n%s", out.String())
	}
}

func TestDomainsVerifyChecksCloudflareDNSRecord(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps.yml")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	t.Setenv("SINGLESERVER_STATE_DIR", dir)
	if err := os.WriteFile(configPath, []byte(`apps:
  - repo: dvassallo/fullsend
    hosts:
      - localhost
`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cloudflare.json"), []byte(`{"api_token":"token","account_id":"account","tunnel_id":"tunnel"}`), 0600); err != nil {
		t.Fatal(err)
	}
	stubCommandRun(t)

	originalVerify := verifyCloudflareDNSRecordFunc
	t.Cleanup(func() { verifyCloudflareDNSRecordFunc = originalVerify })
	verifyCloudflareDNSRecordFunc = func(host string, state *CloudflareState, client *CloudflareClient) (string, error) {
		if host != "localhost" {
			t.Fatalf("unexpected host: %s", host)
		}
		return state.TunnelID + ".cfargotunnel.com", nil
	}

	var out bytes.Buffer
	if err := cliDomains([]string{"verify", "fullsend"}, &out, log.New(io.Discard, "", 0)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "fullsend\tcloudflare_dns\tok\tlocalhost\ttarget=tunnel.cfargotunnel.com") {
		t.Fatalf("expected Cloudflare DNS ok output, got:\n%s", out.String())
	}
}

func TestDomainsVerifySkipsResolverDNSWhenCloudflareDNSRecordIsChecked(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps.yml")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	t.Setenv("SINGLESERVER_STATE_DIR", dir)
	if err := os.WriteFile(configPath, []byte(`apps:
  - repo: dvassallo/fullsend
    hosts:
      - app.example.com
`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cloudflare.json"), []byte(`{"api_token":"token","account_id":"account","tunnel_id":"tunnel"}`), 0600); err != nil {
		t.Fatal(err)
	}

	originalRun := commandRunFunc
	t.Cleanup(func() { commandRunFunc = originalRun })
	commandRunFunc = func(timeout time.Duration, name string, args ...string) error {
		if name == "getent" {
			t.Fatalf("domains verify should not call resolver DNS when Cloudflare DNS can be checked")
		}
		return nil
	}
	originalVerify := verifyCloudflareDNSRecordFunc
	t.Cleanup(func() { verifyCloudflareDNSRecordFunc = originalVerify })
	verifyCloudflareDNSRecordFunc = func(host string, state *CloudflareState, client *CloudflareClient) (string, error) {
		return state.TunnelID + ".cfargotunnel.com", nil
	}

	var out bytes.Buffer
	if err := cliDomains([]string{"verify", "fullsend"}, &out, log.New(io.Discard, "", 0)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "\tdns\t") {
		t.Fatalf("expected resolver DNS output to be skipped, got:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "fullsend\tcloudflare_dns\tok\tapp.example.com\ttarget=tunnel.cfargotunnel.com") {
		t.Fatalf("expected Cloudflare DNS ok output, got:\n%s", out.String())
	}
}

func TestDomainsVerifyFailsWhenCloudflareDNSRecordDoesNotMatch(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps.yml")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	t.Setenv("SINGLESERVER_STATE_DIR", dir)
	if err := os.WriteFile(configPath, []byte(`apps:
  - repo: dvassallo/fullsend
    hosts:
      - localhost
`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cloudflare.json"), []byte(`{"api_token":"token","account_id":"account","tunnel_id":"tunnel"}`), 0600); err != nil {
		t.Fatal(err)
	}
	stubCommandRun(t)

	originalVerify := verifyCloudflareDNSRecordFunc
	t.Cleanup(func() { verifyCloudflareDNSRecordFunc = originalVerify })
	verifyCloudflareDNSRecordFunc = func(host string, state *CloudflareState, client *CloudflareClient) (string, error) {
		return state.TunnelID + ".cfargotunnel.com", errors.New("missing CNAME")
	}

	var out bytes.Buffer
	err := cliDomains([]string{"verify", "fullsend"}, &out, log.New(io.Discard, "", 0))
	if err == nil {
		t.Fatal("expected Cloudflare DNS verification error")
	}
	if !strings.Contains(out.String(), "fullsend\tcloudflare_dns\tfailed\tlocalhost\tmissing CNAME") {
		t.Fatalf("expected Cloudflare DNS failed output, got:\n%s", out.String())
	}
}

func TestDomainsVerifyIgnoresMissingLegacyTunnelRoute(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps.yml")
	tunnelConfigPath := filepath.Join(dir, "cloudflared.yml")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	t.Setenv("SINGLESERVER_STATE_DIR", dir)
	stubCommandRun(t)
	if err := os.WriteFile(configPath, []byte(`apps:
  - repo: dvassallo/fullsend
    hosts:
      - localhost
`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cloudflare.json"), []byte(`{"tunnel_id":"tunnel","config_file":"`+tunnelConfigPath+`"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tunnelConfigPath, []byte(`ingress:
  - service: http_status:404
`), 0600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := cliDomains([]string{"verify", "fullsend"}, &out, log.New(io.Discard, "", 0)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "tunnel_route") {
		t.Fatalf("domains verify should not inspect tunnel routes, got:\n%s", out.String())
	}
}

func TestDomainsVerifyUsesCommandRunFuncForResolverDNS(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps.yml")
	tunnelConfigPath := filepath.Join(dir, "cloudflared.yml")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	t.Setenv("SINGLESERVER_STATE_DIR", dir)
	if err := os.WriteFile(configPath, []byte(`apps:
  - repo: dvassallo/fullsend
    hosts:
      - app.nobrainer.host
`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cloudflare.json"), []byte(`{"tunnel_id":"tunnel","config_file":"`+tunnelConfigPath+`"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tunnelConfigPath, []byte(`ingress:
  - hostname: app.nobrainer.host
    service: http://127.0.0.1:80
  - service: http_status:404
`), 0600); err != nil {
		t.Fatal(err)
	}

	originalRun := commandRunFunc
	t.Cleanup(func() { commandRunFunc = originalRun })
	commandRunFunc = func(timeout time.Duration, name string, args ...string) error {
		if name == "getent" {
			return errors.New("resolver unavailable")
		}
		return originalRun(timeout, name, args...)
	}

	var out bytes.Buffer
	err := cliDomains([]string{"verify", "fullsend"}, &out, log.New(io.Discard, "", 0))
	if err == nil {
		t.Fatal("expected resolver DNS error")
	}
	if !strings.Contains(out.String(), "fullsend\tdns\tfailed\tapp.nobrainer.host\tresolver unavailable") {
		t.Fatalf("expected resolver DNS failure output, got:\n%s", out.String())
	}
}
