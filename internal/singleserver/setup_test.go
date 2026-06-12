package singleserver

import (
	"bytes"
	"encoding/json"
	"html"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGitHubConnectPrintsSetupURL(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SINGLESERVER_STATE_DIR", dir)
	t.Setenv("SINGLESERVER_CONFIG", filepath.Join(dir, "apps.yml"))
	setBaseDirsForTest(t, dir)
	stubCommandRun(t)

	var out bytes.Buffer
	if err := cliGitHubConnect(nil, &out); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "singleserver.env"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "SINGLESERVER_GITHUB_APP_") {
		t.Fatalf("unexpected GitHub App override stored:\n%s", body)
	}
	if !strings.Contains(out.String(), "/setup/github-app?token=") {
		t.Fatalf("setup URL not printed: %s", out.String())
	}
	if !strings.Contains(out.String(), "create/install the GitHub App") {
		t.Fatalf("setup instructions not printed: %s", out.String())
	}
}

func TestTailscaleConnectStoresHostname(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SINGLESERVER_STATE_DIR", dir)
	t.Setenv("SINGLESERVER_CONFIG", filepath.Join(dir, "apps.yml"))
	setBaseDirsForTest(t, dir)
	originalOutput := commandOutputFunc
	originalRun := commandRunFunc
	originalRunToWriter := commandRunToWriterFunc
	originalFunnelReady := tailscaleFunnelReadyFunc
	t.Cleanup(func() {
		commandOutputFunc = originalOutput
		commandRunFunc = originalRun
		commandRunToWriterFunc = originalRunToWriter
		tailscaleFunnelReadyFunc = originalFunnelReady
	})
	commandOutputFunc = func(timeout time.Duration, name string, args ...string) (string, error) {
		if name != "tailscale" {
			t.Fatalf("unexpected output command: %s %s", name, strings.Join(args, " "))
		}
		switch strings.Join(args, " ") {
		case "version":
			return "1.84.0", nil
		case "status --json":
			return `{"BackendState":"Running","Self":{"DNSName":"singleserver.example.ts.net.","HostName":"singleserver"}}`, nil
		default:
			t.Fatalf("unexpected tailscale output args: %s", strings.Join(args, " "))
		}
		return "", nil
	}
	runCommands := []string{}
	commandRunFunc = func(timeout time.Duration, name string, args ...string) error {
		runCommands = append(runCommands, name+" "+strings.Join(args, " "))
		return nil
	}
	writerCommands := []string{}
	commandRunToWriterFunc = func(w io.Writer, timeout time.Duration, name string, args ...string) error {
		writerCommands = append(writerCommands, name+" "+strings.Join(args, " "))
		return nil
	}
	readyChecks := []string{}
	tailscaleFunnelReadyFunc = func(funnelURL string, timeout time.Duration) error {
		readyChecks = append(readyChecks, funnelURL)
		return nil
	}

	var out bytes.Buffer
	if err := cliTailscaleConnect(nil, &out); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "tailscale.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"hostname": "singleserver.example.ts.net"`) {
		t.Fatalf("hostname not stored:\n%s", body)
	}
	if !strings.Contains(string(body), `"funnel_url": "https://singleserver.example.ts.net"`) {
		t.Fatalf("funnel URL not stored:\n%s", body)
	}
	envBody, err := os.ReadFile(filepath.Join(dir, "singleserver.env"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(envBody), "SINGLESERVER_PUBLIC_URL='https://singleserver.example.ts.net'") {
		t.Fatalf("public URL not stored in env:\n%s", envBody)
	}
	if !strings.Contains(strings.Join(writerCommands, "\n"), "tailscale funnel --bg --yes 8787") {
		t.Fatalf("expected funnel command: %#v", writerCommands)
	}
	if strings.Join(readyChecks, "\n") != "https://singleserver.example.ts.net" {
		t.Fatalf("expected Funnel readiness check: %#v", readyChecks)
	}
	if !strings.Contains(out.String(), "tailscale\tssh\tok") {
		t.Fatalf("ssh output missing:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "tailscale\tfunnel\tstarting\t127.0.0.1:8787") {
		t.Fatalf("funnel starting output missing:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "tailscale\tfunnel\tok\thttps://singleserver.example.ts.net\ttarget=127.0.0.1:8787") {
		t.Fatalf("funnel output missing:\n%s", out.String())
	}
}

func setBaseDirsForTest(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("SINGLESERVER_REPOS_ROOT", filepath.Join(dir, "repos"))
	t.Setenv("SINGLESERVER_STORAGE_ROOT", filepath.Join(dir, "storage"))
	t.Setenv("SINGLESERVER_BACKUP_DIR", filepath.Join(dir, "backups"))
}

func TestSetupGitHubAppManifestIsPublic(t *testing.T) {
	t.Setenv("SINGLESERVER_GITHUB_APP_NAME", "Single Server test-host")
	server := &Server{
		publicURL:  "https://singleserver.example.ts.net",
		setupToken: "setup-token",
	}
	req := httptest.NewRequest("GET", "/setup/github-app?token=setup-token", nil)
	res := httptest.NewRecorder()

	server.handleSetupGitHubApp(res, req)

	body := res.Body.String()
	if !strings.Contains(body, "public GitHub App") {
		t.Fatalf("expected public app copy:\n%s", body)
	}
	manifest := setupManifestFromBody(t, body)
	if manifest["public"] != true {
		t.Fatalf("expected public manifest, got %#v", manifest["public"])
	}
	if manifest["name"] != "Single Server test-host" {
		t.Fatalf("expected custom app name, got %#v", manifest["name"])
	}
}

func TestSetupGitHubAppManifestCanBePrivate(t *testing.T) {
	t.Setenv("SINGLESERVER_GITHUB_APP_NAME", "Single Server E2E")
	t.Setenv("SINGLESERVER_GITHUB_APP_PUBLIC", "false")
	server := &Server{
		publicURL:  "https://singleserver.example.ts.net",
		setupToken: "setup-token",
	}
	req := httptest.NewRequest("GET", "/setup/github-app?token=setup-token", nil)
	res := httptest.NewRecorder()

	server.handleSetupGitHubApp(res, req)

	body := res.Body.String()
	if !strings.Contains(body, "private GitHub App") {
		t.Fatalf("expected private app copy:\n%s", body)
	}
	manifest := setupManifestFromBody(t, body)
	if manifest["public"] != false {
		t.Fatalf("expected private manifest, got %#v", manifest["public"])
	}
}

func TestGitHubAppNameFromHostname(t *testing.T) {
	tests := []struct {
		hostname string
		want     string
	}{
		{hostname: "ubuntu-2gb-hil-2", want: "Single Server ubuntu-2gb-hil-2"},
		{hostname: "Single_Server.local", want: "Single Server single-server-local"},
		{hostname: "singleserver-e2e-bootstrap-20260609204840", want: "Single Server singleserver-e2e-boo"},
		{hostname: "", want: "Single Server"},
	}

	for _, test := range tests {
		if got := githubAppNameFromHostname(test.hostname); got != test.want {
			t.Fatalf("githubAppNameFromHostname(%q) = %q, want %q", test.hostname, got, test.want)
		}
	}
}

func TestApplyCloudflareTunnelNamePreservesExistingTunnel(t *testing.T) {
	state := &CloudflareState{
		TunnelName:   "singleserver",
		TunnelID:     "tunnel-id",
		TunnelSecret: "secret",
	}

	applyCloudflareTunnelName(state, "singleserver", false)

	if state.TunnelID != "tunnel-id" || state.TunnelSecret != "secret" {
		t.Fatalf("expected tunnel state preserved: %#v", state)
	}
}

func TestApplyCloudflareTunnelNameClearsDifferentNamedTunnel(t *testing.T) {
	state := &CloudflareState{
		TunnelName:   "old",
		TunnelID:     "tunnel-id",
		TunnelSecret: "secret",
	}

	applyCloudflareTunnelName(state, "new", true)

	if state.TunnelName != "new" {
		t.Fatalf("unexpected tunnel name: %s", state.TunnelName)
	}
	if state.TunnelID != "" || state.TunnelSecret != "" {
		t.Fatalf("expected tunnel credentials cleared: %#v", state)
	}
}

func TestApplyCloudflareTunnelNameClearsUnknownTunnelWhenExplicit(t *testing.T) {
	state := &CloudflareState{
		TunnelID:     "tunnel-id",
		TunnelSecret: "secret",
	}

	applyCloudflareTunnelName(state, "new", true)

	if state.TunnelName != "new" {
		t.Fatalf("unexpected tunnel name: %s", state.TunnelName)
	}
	if state.TunnelID != "" || state.TunnelSecret != "" {
		t.Fatalf("expected tunnel credentials cleared: %#v", state)
	}
}

func TestCloudflareTunnelNameFromHostname(t *testing.T) {
	tests := []struct {
		hostname string
		want     string
	}{
		{hostname: "ubuntu-2gb-hil-2", want: "singleserver-ubuntu-2gb-hil-2"},
		{hostname: "Single_Server.local", want: "singleserver-single-server-local"},
		{hostname: "", want: "singleserver"},
	}

	for _, test := range tests {
		if got := cloudflareTunnelNameFromHostname(test.hostname); got != test.want {
			t.Fatalf("cloudflareTunnelNameFromHostname(%q) = %q, want %q", test.hostname, got, test.want)
		}
	}
}

func TestAccountIDFromZonesUsesSingleAccessibleAccount(t *testing.T) {
	zones := []cloudflareZone{
		{ID: "zone-a", Name: "example.com"},
		{ID: "zone-b", Name: "example.net"},
	}
	zones[0].Account.ID = "account"
	zones[1].Account.ID = "account"

	got, err := accountIDFromZones(zones)
	if err != nil {
		t.Fatal(err)
	}
	if got != "account" {
		t.Fatalf("unexpected account id: %s", got)
	}
}

func TestAccountIDFromZonesRejectsMultipleAccounts(t *testing.T) {
	zones := []cloudflareZone{
		{ID: "zone-a", Name: "example.com"},
		{ID: "zone-b", Name: "example.net"},
	}
	zones[0].Account.ID = "account-a"
	zones[1].Account.ID = "account-b"

	_, err := accountIDFromZones(zones)
	if err == nil || !strings.Contains(err.Error(), "multiple accounts") {
		t.Fatalf("expected multiple account error, got %v", err)
	}
}

func TestTailscaleStatusNameFallbacks(t *testing.T) {
	tests := []struct {
		name   string
		status *tailscaleStatus
		want   string
	}{
		{name: "nil", status: nil, want: "-"},
		{name: "nil self", status: &tailscaleStatus{}, want: "-"},
		{name: "dns name", status: testTailscaleStatus(" server.tailnet.ts.net. ", "server"), want: "server.tailnet.ts.net"},
		{name: "hostname fallback", status: testTailscaleStatus("", " server "), want: "server"},
		{name: "blank", status: testTailscaleStatus(" ", " "), want: "-"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := tailscaleStatusName(test.status); got != test.want {
				t.Fatalf("tailscaleStatusName() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestTailscaleFunnelURLRequiresTsNetHostname(t *testing.T) {
	tests := []struct {
		name   string
		status *tailscaleStatus
		want   string
	}{
		{name: "ts net dns", status: testTailscaleStatus("server.tailnet.ts.net.", ""), want: "https://server.tailnet.ts.net"},
		{name: "non ts net hostname", status: testTailscaleStatus("", "server.example.com"), want: ""},
		{name: "missing status", status: nil, want: ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := tailscaleFunnelURL(test.status); got != test.want {
				t.Fatalf("tailscaleFunnelURL() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestLoadTailscaleState(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SINGLESERVER_STATE_DIR", dir)

	missing, err := loadTailscaleState()
	if err != nil {
		t.Fatal(err)
	}
	if missing.Hostname != "" || missing.FunnelURL != "" {
		t.Fatalf("expected empty missing state, got %#v", missing)
	}

	if err := os.WriteFile(filepath.Join(dir, "tailscale.json"), []byte(`{"hostname":"server.tailnet.ts.net","funnel_url":"https://server.tailnet.ts.net"}`), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadTailscaleState()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Hostname != "server.tailnet.ts.net" || loaded.FunnelURL != "https://server.tailnet.ts.net" {
		t.Fatalf("unexpected loaded state: %#v", loaded)
	}

	if err := os.WriteFile(filepath.Join(dir, "tailscale.json"), []byte(`{`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadTailscaleState(); err == nil {
		t.Fatal("expected invalid JSON error")
	}
}

func TestSetupFlagTakesValue(t *testing.T) {
	tests := []struct {
		name string
		fn   func(string) bool
		arg  string
		want bool
	}{
		{name: "cloudflare account", fn: cloudflareFlagTakesValue, arg: "--account", want: true},
		{name: "cloudflare account equals", fn: cloudflareFlagTakesValue, arg: "--account=abc", want: true},
		{name: "cloudflare tunnel", fn: cloudflareFlagTakesValue, arg: "--tunnel", want: true},
		{name: "cloudflare non interactive", fn: cloudflareFlagTakesValue, arg: "--non-interactive", want: false},
		{name: "github has no value flags", fn: githubFlagTakesValue, arg: "--setup-token", want: false},
		{name: "tailscale auth key", fn: tailscaleFlagTakesValue, arg: "--auth-key", want: true},
		{name: "tailscale auth key equals", fn: tailscaleFlagTakesValue, arg: "--auth-key=tskey", want: true},
		{name: "tailscale hostname", fn: tailscaleFlagTakesValue, arg: "--hostname", want: true},
		{name: "tailscale non interactive", fn: tailscaleFlagTakesValue, arg: "--non-interactive", want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.fn(test.arg); got != test.want {
				t.Fatalf("flag takes value = %v, want %v", got, test.want)
			}
		})
	}
}

func setupManifestFromBody(t *testing.T, body string) map[string]any {
	t.Helper()
	const prefix = `name="manifest" value="`
	start := strings.Index(body, prefix)
	if start < 0 {
		t.Fatalf("manifest input missing:\n%s", body)
	}
	start += len(prefix)
	end := strings.Index(body[start:], `"`)
	if end < 0 {
		t.Fatalf("manifest input unterminated:\n%s", body)
	}
	encoded := body[start : start+end]
	decoded := html.UnescapeString(encoded)
	var manifest map[string]any
	if err := json.Unmarshal([]byte(decoded), &manifest); err != nil {
		t.Fatalf("manifest did not decode: %v\n%s", err, decoded)
	}
	return manifest
}

func stubCommandRun(t *testing.T) {
	t.Helper()
	original := commandRunFunc
	t.Cleanup(func() { commandRunFunc = original })
	commandRunFunc = func(timeout time.Duration, name string, args ...string) error {
		return nil
	}
}

func testTailscaleStatus(dnsName, hostName string) *tailscaleStatus {
	return &tailscaleStatus{
		BackendState: "Running",
		Self: &struct {
			DNSName      string   `json:"DNSName"`
			HostName     string   `json:"HostName"`
			TailscaleIPs []string `json:"TailscaleIPs"`
		}{
			DNSName:  dnsName,
			HostName: hostName,
		},
	}
}
