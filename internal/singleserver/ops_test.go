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

func TestDomainsAndStorageCommandsUpdateConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps.yml")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	stubCommandRun(t)
	if err := os.WriteFile(configPath, []byte("apps:\n  - dvassallo/fullsend\n"), 0600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	logger := log.New(io.Discard, "", 0)
	if err := cliDomains([]string{"add", "fullsend", "play.nobrainer.host", "--no-deploy"}, &out, logger); err != nil {
		t.Fatal(err)
	}
	storagePath := filepath.Join(dir, "storage")
	if err := cliStorage([]string{"enable", "fullsend", "--path", storagePath, "--mount", "/data", "--no-deploy"}, &out, logger); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "fullsend\tnext\tdeploy with `singleserver deploy dvassallo/fullsend`") {
		t.Fatalf("expected staged deploy message, got:\n%s", out.String())
	}

	config, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	app := config.Apps[0]
	if len(app.Hosts) != 1 || app.Hosts[0] != "play.nobrainer.host" {
		t.Fatalf("unexpected hosts: %#v", app.Hosts)
	}
	if app.Healthcheck != "https://play.nobrainer.host/up" {
		t.Fatalf("unexpected healthcheck: %s", app.Healthcheck)
	}
	if app.Storage == nil || app.Storage.Path != storagePath || app.Storage.Mount != "/data" {
		t.Fatalf("unexpected storage: %#v", app.Storage)
	}
}

func TestStorageEnableFailsBeforeConfigWriteWhenOwnershipFixFails(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps.yml")
	storagePath := filepath.Join(dir, "storage")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	if err := os.WriteFile(configPath, []byte("apps:\n  - dvassallo/fullsend\n"), 0600); err != nil {
		t.Fatal(err)
	}

	originalRun := commandRunFunc
	t.Cleanup(func() { commandRunFunc = originalRun })
	commandRunFunc = func(timeout time.Duration, name string, args ...string) error {
		return errors.New("chown failed")
	}
	originalWriteConfig := writeConfigFunc
	t.Cleanup(func() { writeConfigFunc = originalWriteConfig })
	writeConfigCalled := false
	writeConfigFunc = func(path string, config *Config) error {
		writeConfigCalled = true
		return originalWriteConfig(path, config)
	}

	var out bytes.Buffer
	logger := log.New(io.Discard, "", 0)
	err := cliStorage([]string{"enable", "fullsend", "--path", storagePath, "--no-deploy"}, &out, logger)
	if err == nil {
		t.Fatal("expected chown error")
	}
	if !strings.Contains(err.Error(), "chown "+storagePath+" to deploy:docker") {
		t.Fatalf("unexpected error: %v", err)
	}
	if writeConfigCalled {
		t.Fatal("did not expect config write after chown failure")
	}
	if strings.Contains(out.String(), "storage\tok") {
		t.Fatalf("unexpected success output: %s", out.String())
	}
	if _, err := os.Stat(storagePath); !os.IsNotExist(err) {
		t.Fatalf("expected newly-created storage directory to be removed, stat err=%v", err)
	}
}

func TestStorageEnableReportsSuccessOnlyAfterConfigWrite(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps.yml")
	storagePath := filepath.Join(dir, "storage")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	stubCommandRun(t)
	if err := os.WriteFile(configPath, []byte("apps:\n  - dvassallo/fullsend\n"), 0600); err != nil {
		t.Fatal(err)
	}

	originalWriteConfig := writeConfigFunc
	t.Cleanup(func() { writeConfigFunc = originalWriteConfig })
	writeConfigFunc = func(path string, config *Config) error {
		return errors.New("config write failed")
	}

	var out bytes.Buffer
	logger := log.New(io.Discard, "", 0)
	err := cliStorage([]string{"enable", "fullsend", "--path", storagePath, "--no-deploy"}, &out, logger)
	if err == nil {
		t.Fatal("expected config write error")
	}
	if !strings.Contains(err.Error(), "config write failed") {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(out.String(), "storage\tok") {
		t.Fatalf("unexpected success output: %s", out.String())
	}
	if _, err := os.Stat(storagePath); !os.IsNotExist(err) {
		t.Fatalf("expected newly-created storage directory to be removed, stat err=%v", err)
	}
}

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
	if err := os.WriteFile(filepath.Join(dir, "cloudflare.json"), []byte(`{"api_token":"token","zone_id":"zone","server_ip":"203.0.113.10"}`), 0600); err != nil {
		t.Fatal(err)
	}

	originalVerify := verifyCloudflareDNSRecordFunc
	t.Cleanup(func() { verifyCloudflareDNSRecordFunc = originalVerify })
	verifyCloudflareDNSRecordFunc = func(host string, state *CloudflareState, client *CloudflareClient) (string, error) {
		if host != "localhost" {
			t.Fatalf("unexpected host: %s", host)
		}
		return state.ServerIP, nil
	}

	var out bytes.Buffer
	if err := cliDomains([]string{"verify", "fullsend"}, &out, log.New(io.Discard, "", 0)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "fullsend\tcloudflare_dns\tok\tlocalhost -> 203.0.113.10") {
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
	if err := os.WriteFile(filepath.Join(dir, "cloudflare.json"), []byte(`{"api_token":"token","zone_id":"zone","server_ip":"203.0.113.10"}`), 0600); err != nil {
		t.Fatal(err)
	}

	originalVerify := verifyCloudflareDNSRecordFunc
	t.Cleanup(func() { verifyCloudflareDNSRecordFunc = originalVerify })
	verifyCloudflareDNSRecordFunc = func(host string, state *CloudflareState, client *CloudflareClient) (string, error) {
		return state.ServerIP, errors.New("missing A record")
	}

	var out bytes.Buffer
	err := cliDomains([]string{"verify", "fullsend"}, &out, log.New(io.Discard, "", 0))
	if err == nil {
		t.Fatal("expected Cloudflare DNS verification error")
	}
	if !strings.Contains(out.String(), "fullsend\tcloudflare_dns\tfailed\tlocalhost\tmissing A record") {
		t.Fatalf("expected Cloudflare DNS failed output, got:\n%s", out.String())
	}
}

func TestDomainsVerifyIgnoresMissingLegacyTunnelRoute(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps.yml")
	tunnelConfigPath := filepath.Join(dir, "cloudflared.yml")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	t.Setenv("SINGLESERVER_STATE_DIR", dir)
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

func TestEnvCommandWritesServerSideEnv(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps.yml")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	t.Setenv("SINGLESERVER_STATE_DIR", dir)
	if err := os.WriteFile(configPath, []byte("apps:\n  - dvassallo/fullsend\n"), 0600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := cliEnv([]string{"set", "fullsend", "DATABASE_URL=sqlite:///storage/app.db"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "fullsend\tnext\tdeploy with `singleserver deploy dvassallo/fullsend`") {
		t.Fatalf("expected next deploy message, got:\n%s", out.String())
	}
	values, err := loadAppEnv("fullsend")
	if err != nil {
		t.Fatal(err)
	}
	if values["DATABASE_URL"] != "sqlite:///storage/app.db" {
		t.Fatalf("unexpected DATABASE_URL: %q", values["DATABASE_URL"])
	}
}

func TestBackupAndRestoreStorageReplacesDirectory(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps.yml")
	storagePath := filepath.Join(dir, "storage")
	backupRoot := filepath.Join(dir, "backups")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	t.Setenv("SINGLESERVER_BACKUP_DIR", backupRoot)
	stubCommandRun(t)
	if err := os.MkdirAll(filepath.Join(storagePath, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storagePath, "data.txt"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storagePath, "nested", "keep.txt"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`apps:
  - repo: dvassallo/fullsend
    storage:
      path: `+storagePath+`
      mount: /storage
`), 0600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := cliBackup([]string{"fullsend"}, &out); err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(out.String())
	if len(fields) < 4 {
		t.Fatalf("unexpected backup output: %q", out.String())
	}
	backupPath := fields[3]

	if err := os.WriteFile(filepath.Join(storagePath, "data.txt"), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storagePath, "extra.txt"), []byte("extra"), 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := cliRestore([]string{"fullsend", backupPath, "--yes", "--no-restart"}, &out); err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(filepath.Join(storagePath, "data.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "old" {
		t.Fatalf("expected restored content, got %q", string(body))
	}
	if _, err := os.Stat(filepath.Join(storagePath, "extra.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected extra file removed, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(storagePath, "nested", "keep.txt")); err != nil {
		t.Fatalf("expected nested file restored: %v", err)
	}
}

func TestRestoreFailsBeforeReplacingStorageWhenOwnershipFixFails(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps.yml")
	storagePath := filepath.Join(dir, "storage")
	backupRoot := filepath.Join(dir, "backups")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	t.Setenv("SINGLESERVER_BACKUP_DIR", backupRoot)
	if err := os.MkdirAll(storagePath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storagePath, "data.txt"), []byte("backup"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`apps:
  - repo: dvassallo/fullsend
    storage:
      path: `+storagePath+`
      mount: /storage
`), 0600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := cliBackup([]string{"fullsend"}, &out); err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(out.String())
	if len(fields) < 4 {
		t.Fatalf("unexpected backup output: %q", out.String())
	}
	backupPath := fields[3]
	if err := os.WriteFile(filepath.Join(storagePath, "data.txt"), []byte("current"), 0600); err != nil {
		t.Fatal(err)
	}

	originalRun := commandRunFunc
	t.Cleanup(func() { commandRunFunc = originalRun })
	commandRunFunc = func(timeout time.Duration, name string, args ...string) error {
		return errors.New("chown failed")
	}
	out.Reset()
	err := cliRestore([]string{"fullsend", backupPath, "--yes", "--no-restart"}, &out)
	if err == nil {
		t.Fatal("expected chown error")
	}
	if !strings.Contains(err.Error(), "chown ") {
		t.Fatalf("unexpected error: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(storagePath, "data.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "current" {
		t.Fatalf("expected current storage to remain untouched, got %q", string(body))
	}
	if strings.Contains(out.String(), "restore\tok") {
		t.Fatalf("unexpected restore success output: %s", out.String())
	}
}

func TestRestoreRequiresConfirmation(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps.yml")
	storagePath := filepath.Join(dir, "storage")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	if err := os.MkdirAll(storagePath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`apps:
  - repo: dvassallo/fullsend
    storage:
      path: `+storagePath+`
      mount: /storage
`), 0600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err := cliRestore([]string{"fullsend", filepath.Join(dir, "missing.tar.gz"), "--no-restart"}, &out)
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("expected --yes confirmation error, got %v", err)
	}
}

func TestRemoveDeleteStorageRequiresConfirmation(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps.yml")
	storagePath := filepath.Join(dir, "storage")
	repoPath := filepath.Join(dir, "repos", "fullsend")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	t.Setenv("SINGLESERVER_STATE_DIR", dir)
	if err := os.MkdirAll(storagePath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(repoPath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`apps:
  - repo: dvassallo/fullsend
    path: `+repoPath+`
    storage:
      path: `+storagePath+`
      mount: /storage
`), 0600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err := cliRemove([]string{"fullsend", "--delete-storage"}, &out)
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("expected --yes confirmation error, got %v", err)
	}
	if _, err := os.Stat(storagePath); err != nil {
		t.Fatalf("expected storage kept: %v", err)
	}
	if _, err := os.Stat(repoPath); err != nil {
		t.Fatalf("expected repo checkout kept: %v", err)
	}
	config, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Apps) != 1 {
		t.Fatalf("expected app config kept, got %#v", config.Apps)
	}
}

func TestRemoveKeepsConfigWhenCloudflareFails(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps.yml")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	t.Setenv("SINGLESERVER_STATE_DIR", dir)
	if err := os.WriteFile(configPath, []byte(`apps:
  - repo: dvassallo/fullsend
    hosts:
      - play.nobrainer.host
`), 0600); err != nil {
		t.Fatal(err)
	}

	originalSync := syncCloudflareAppDomainFunc
	t.Cleanup(func() { syncCloudflareAppDomainFunc = originalSync })
	syncCloudflareAppDomainFunc = func(hostname string, add bool, w io.Writer) error {
		if !add {
			return errors.New("cloudflare unavailable")
		}
		return nil
	}

	var out bytes.Buffer
	err := cliRemove([]string{"fullsend"}, &out)
	if err == nil {
		t.Fatal("expected Cloudflare error")
	}
	if !strings.Contains(err.Error(), "cloudflare unavailable") {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(out.String(), "config\tok\tremoved") {
		t.Fatalf("unexpected removal output: %s", out.String())
	}

	config, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Apps) != 1 || config.Apps[0].Repo != "dvassallo/fullsend" {
		t.Fatalf("expected app config kept, got %#v", config.Apps)
	}
}

func TestRemoveKeepsFilesWhenContainerStopFails(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps.yml")
	storagePath := filepath.Join(dir, "storage")
	repoPath := filepath.Join(dir, "repos", "fullsend")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	t.Setenv("SINGLESERVER_STATE_DIR", dir)
	if err := os.MkdirAll(storagePath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(repoPath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`apps:
  - repo: dvassallo/fullsend
    path: `+repoPath+`
    storage:
      path: `+storagePath+`
      mount: /storage
`), 0600); err != nil {
		t.Fatal(err)
	}

	originalStop := stopAppContainersFunc
	t.Cleanup(func() { stopAppContainersFunc = originalStop })
	stopAppContainersFunc = func(appName string) error {
		return errors.New("docker unavailable")
	}

	var out bytes.Buffer
	err := cliRemove([]string{"fullsend", "--delete-storage", "--delete-repo", "--yes"}, &out)
	if err == nil {
		t.Fatal("expected container stop error")
	}
	if !strings.Contains(err.Error(), "docker unavailable") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(storagePath); err != nil {
		t.Fatalf("expected storage kept: %v", err)
	}
	if _, err := os.Stat(repoPath); err != nil {
		t.Fatalf("expected repo checkout kept: %v", err)
	}
}

func TestRemoveDeleteStorageWithConfirmation(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps.yml")
	storagePath := filepath.Join(dir, "storage")
	repoPath := filepath.Join(dir, "repos", "fullsend")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	t.Setenv("SINGLESERVER_STATE_DIR", dir)
	if err := os.MkdirAll(storagePath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(repoPath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storagePath, "data.txt"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoPath, "README.md"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`apps:
  - repo: dvassallo/fullsend
    path: `+repoPath+`
    storage:
      path: `+storagePath+`
      mount: /storage
`), 0600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := cliRemove([]string{"fullsend", "--delete-storage", "--delete-repo", "--yes"}, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(storagePath); !os.IsNotExist(err) {
		t.Fatalf("expected storage deleted, stat err=%v", err)
	}
	if _, err := os.Stat(repoPath); !os.IsNotExist(err) {
		t.Fatalf("expected repo checkout deleted, stat err=%v", err)
	}
	config, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Apps) != 0 {
		t.Fatalf("expected app config removed, got %#v", config.Apps)
	}
}
