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
}

func TestTailscaleConnectStoresHostname(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SINGLESERVER_STATE_DIR", dir)
	t.Setenv("SINGLESERVER_CONFIG", filepath.Join(dir, "apps.yml"))
	originalOutput := commandOutputFunc
	originalRun := commandRunFunc
	originalRunToWriter := commandRunToWriterFunc
	t.Cleanup(func() {
		commandOutputFunc = originalOutput
		commandRunFunc = originalRun
		commandRunToWriterFunc = originalRunToWriter
	})
	commandOutputFunc = func(timeout time.Duration, name string, args ...string) (string, error) {
		if name != "tailscale" {
			t.Fatalf("unexpected output command: %s %s", name, strings.Join(args, " "))
		}
		switch strings.Join(args, " ") {
		case "version":
			return "1.84.0", nil
		case "status --json":
			return `{"BackendState":"Running","Self":{"DNSName":"assetstacks.example.ts.net.","HostName":"assetstacks"}}`, nil
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

	var out bytes.Buffer
	if err := cliTailscaleConnect(nil, &out); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "tailscale.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"hostname": "assetstacks.example.ts.net"`) {
		t.Fatalf("hostname not stored:\n%s", body)
	}
	if !strings.Contains(string(body), `"funnel_url": "https://assetstacks.example.ts.net"`) {
		t.Fatalf("funnel URL not stored:\n%s", body)
	}
	envBody, err := os.ReadFile(filepath.Join(dir, "singleserver.env"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(envBody), "SINGLESERVER_PUBLIC_URL='https://assetstacks.example.ts.net'") {
		t.Fatalf("public URL not stored in env:\n%s", envBody)
	}
	if !strings.Contains(strings.Join(writerCommands, "\n"), "tailscale funnel --bg --yes 8787") {
		t.Fatalf("expected funnel command: %#v", writerCommands)
	}
	if !strings.Contains(out.String(), "tailscale\tssh\tok") {
		t.Fatalf("ssh output missing:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "tailscale\tfunnel\tstarting\t127.0.0.1:8787") {
		t.Fatalf("funnel starting output missing:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "tailscale\tfunnel\tok\thttps://assetstacks.example.ts.net -> 127.0.0.1:8787") {
		t.Fatalf("funnel output missing:\n%s", out.String())
	}
}

func TestSetupGitHubAppManifestIsPublic(t *testing.T) {
	server := &Server{
		publicURL:  "https://assetstacks.example.ts.net",
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
