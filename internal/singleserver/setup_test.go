package singleserver

import (
	"bytes"
	"encoding/json"
	"html"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGitHubConnectStoresCustomAppName(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SINGLESERVER_STATE_DIR", dir)
	t.Setenv("SINGLESERVER_CONFIG", filepath.Join(dir, "apps.yml"))
	stubCommandRun(t)

	var out bytes.Buffer
	if err := cliGitHubConnect([]string{"--name", "Single Server Test"}, &out); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "singleserver.env"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "SINGLESERVER_GITHUB_APP_NAME='Single Server Test'") {
		t.Fatalf("custom app name not stored:\n%s", body)
	}
	if !strings.Contains(out.String(), "/setup/github-app?token=") {
		t.Fatalf("setup URL not printed: %s", out.String())
	}
}

func TestGitHubConnectStoresPublicAppFlag(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SINGLESERVER_STATE_DIR", dir)
	t.Setenv("SINGLESERVER_CONFIG", filepath.Join(dir, "apps.yml"))
	stubCommandRun(t)

	var out bytes.Buffer
	if err := cliGitHubConnect([]string{"--public"}, &out); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "singleserver.env"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "SINGLESERVER_GITHUB_APP_PUBLIC='true'") {
		t.Fatalf("public app flag not stored:\n%s", body)
	}
}

func TestSetupGitHubAppManifestCanBePublic(t *testing.T) {
	t.Setenv("SINGLESERVER_GITHUB_APP_PUBLIC", "true")
	server := &Server{
		publicURL:  "https://hooks.example.com",
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
