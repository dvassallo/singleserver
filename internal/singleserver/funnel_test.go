package singleserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func writeTailnetState(t *testing.T, extra string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("SINGLESERVER_STATE_DIR", dir)
	body := `{"hostname":"server.corp.ts.net","funnel_url":"https://server.corp.ts.net"` + extra + `}`
	if err := os.WriteFile(filepath.Join(dir, "tailscale.json"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func stubFunnelContainerState(t *testing.T, state funnelContainerState) {
	t.Helper()
	original := funnelContainerStateFunc
	t.Cleanup(func() { funnelContainerStateFunc = original })
	funnelContainerStateFunc = func(AppConfig) funnelContainerState { return state }
}

func funnelApp() AppConfig {
	return AppConfig{
		Repo:   "acme/ledger",
		Name:   "ledger",
		Hosts:  []string{"ledger"},
		Tunnel: TunnelPrivate,
		Funnel: &FunnelConfig{Host: "ledger-mcp", Paths: []string{"/mcp", "/.well-known/oauth-protected-resource"}},
	}
}

func TestNormalizeFunnelDefaultsHostAndCleansPaths(t *testing.T) {
	writeTailnetState(t, "")
	app := AppConfig{Repo: "acme/ledger", Tunnel: TunnelPrivate, Funnel: &FunnelConfig{Paths: []string{" /mcp/ ", "/mcp", "/hooks"}}}
	if err := app.Normalize(); err != nil {
		t.Fatal(err)
	}
	if app.Funnel.Host != "ledger-public" {
		t.Fatalf("expected default funnel host, got %q", app.Funnel.Host)
	}
	if got := strings.Join(app.Funnel.Paths, ","); got != "/mcp,/hooks" {
		t.Fatalf("expected cleaned, deduplicated paths, got %q", got)
	}
	if app.FunnelHost() != "ledger-public.corp.ts.net" || app.FunnelURL() != "https://ledger-public.corp.ts.net/mcp" {
		t.Fatalf("unexpected funnel host/url: %s %s", app.FunnelHost(), app.FunnelURL())
	}
	if got := strings.Join(app.ProxyHosts(), ","); got != "ledger-public.corp.ts.net" {
		t.Fatalf("expected the funnel host among proxy hosts, got %q", got)
	}
}

func TestNormalizeFunnelRejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		app  AppConfig
		want string
	}{
		{"public app", AppConfig{Repo: "acme/a", Funnel: &FunnelConfig{Paths: []string{"/x"}}}, "requires tunnel: private"},
		{"no paths", AppConfig{Repo: "acme/a", Tunnel: TunnelPrivate, Funnel: &FunnelConfig{Paths: []string{" "}}}, "at least one path"},
		{"relative path", AppConfig{Repo: "acme/a", Tunnel: TunnelPrivate, Funnel: &FunnelConfig{Paths: []string{"mcp"}}}, "invalid funnel path"},
		{"double slash", AppConfig{Repo: "acme/a", Tunnel: TunnelPrivate, Funnel: &FunnelConfig{Paths: []string{"//"}}}, "invalid funnel path"},
		{"dot dot", AppConfig{Repo: "acme/a", Tunnel: TunnelPrivate, Funnel: &FunnelConfig{Paths: []string{"/mcp/../x"}}}, "invalid funnel path"},
		{"dot segment", AppConfig{Repo: "acme/a", Tunnel: TunnelPrivate, Funnel: &FunnelConfig{Paths: []string{"/./mcp"}}}, "invalid funnel path"},
		{"inner double slash", AppConfig{Repo: "acme/a", Tunnel: TunnelPrivate, Funnel: &FunnelConfig{Paths: []string{"/a//b"}}}, "invalid funnel path"},
		{"query in path", AppConfig{Repo: "acme/a", Tunnel: TunnelPrivate, Funnel: &FunnelConfig{Paths: []string{"/mcp?x=1"}}}, "invalid funnel path"},
		{"host is the app", AppConfig{Repo: "acme/a", Tunnel: TunnelPrivate, Funnel: &FunnelConfig{Host: "a", Paths: []string{"/x"}}}, "must differ from the app's own name"},
		{"host with url", AppConfig{Repo: "acme/a", Tunnel: TunnelPrivate, Funnel: &FunnelConfig{Host: "https://a-mcp", Paths: []string{"/x"}}}, "invalid funnel host"},
		{"public domain host", AppConfig{Repo: "acme/a", Tunnel: TunnelPrivate, Funnel: &FunnelConfig{Host: "a.example.com", Paths: []string{"/x"}}}, ".ts.net name or a bare label"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.app.Normalize()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestLoadConfigRejectsFunnelHostCollidingWithAnotherApp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "apps.yml")
	body := []byte(`apps:
  - repo: alice/notes
    tunnel: private
  - repo: bob/ledger
    tunnel: private
    funnel:
      host: notes
      paths: [/mcp]
`)
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "duplicate tailscale name") {
		t.Fatalf("expected duplicate tailscale name error, got %v", err)
	}
}

func TestLoadConfigRoundTripsFunnel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "apps.yml")
	t.Setenv("SINGLESERVER_STATE_DIR", dir)
	config := &Config{Apps: []AppConfig{funnelApp()}}
	if err := writeConfig(path, config); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "funnel:\n      host: ledger-mcp\n      paths:\n        - /mcp\n") {
		t.Fatalf("expected funnel block in config:\n%s", body)
	}
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Apps[0].HasFunnel() || loaded.Apps[0].Funnel.Host != "ledger-mcp" || len(loaded.Apps[0].Funnel.Paths) != 2 {
		t.Fatalf("unexpected funnel after reload: %#v", loaded.Apps[0].Funnel)
	}
}

func TestGeneratedDeployYAMLAddsFunnelAccessory(t *testing.T) {
	writeTailnetState(t, "")
	t.Setenv("SINGLESERVER_FUNNEL_ROOT", "/var/lib/singleserver/funnel")
	body, err := GeneratedDeployYAML(funnelApp())
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := yaml.Unmarshal(body, &config); err != nil {
		t.Fatal(err)
	}
	hosts := config["proxy"].(map[string]any)["hosts"].([]any)
	if len(hosts) != 2 || hosts[0] != "ledger.corp.ts.net" || hosts[1] != "ledger-mcp.corp.ts.net" {
		t.Fatalf("expected app and funnel hosts on the proxy, got %#v", hosts)
	}
	accessory := config["accessories"].(map[string]any)["funnel"].(map[string]any)
	if accessory["image"] != funnelImage || accessory["host"] != "127.0.0.1" {
		t.Fatalf("unexpected accessory: %#v", accessory)
	}
	env := accessory["env"].(map[string]any)
	clear := env["clear"].(map[string]any)
	if clear["TS_HOSTNAME"] != "ledger-mcp" || clear["TS_USERSPACE"] != "true" || clear["TS_AUTH_ONCE"] != "true" || clear["TS_SERVE_CONFIG"] != "/config/serve.json" {
		t.Fatalf("unexpected accessory env: %#v", clear)
	}
	if clear["TS_EXTRA_ARGS"] != "--advertise-tags="+tailscaleServiceTag || clear[funnelRevisionEnv] == "" {
		t.Fatalf("expected tag and revision in env: %#v", clear)
	}
	if secret := env["secret"].([]any); len(secret) != 1 || secret[0] != "TS_AUTHKEY" {
		t.Fatalf("expected the auth key as the only secret, got %#v", secret)
	}
	volumes := accessory["volumes"].([]any)
	if len(volumes) != 2 || volumes[0] != "/var/lib/singleserver/funnel/ledger/state:/var/lib/tailscale" || volumes[1] != "/var/lib/singleserver/funnel/ledger/config:/config:ro" {
		t.Fatalf("unexpected volumes: %#v", volumes)
	}

	plain := funnelApp()
	plain.Funnel = nil
	body, err = GeneratedDeployYAML(plain)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "accessories") {
		t.Fatalf("apps without a funnel must not get an accessory:\n%s", body)
	}
}

func TestFunnelServeConfigMountsOnlyConfiguredPaths(t *testing.T) {
	app := funnelApp()
	app.Funnel.Paths = append(app.Funnel.Paths, "/")
	body, err := funnelServeConfig(app)
	if err != nil {
		t.Fatal(err)
	}
	var config tailscaleServeConfig
	if err := json.Unmarshal(body, &config); err != nil {
		t.Fatal(err)
	}
	if !config.TCP["443"].HTTPS || !config.AllowFunnel["${TS_CERT_DOMAIN}:443"] {
		t.Fatalf("expected HTTPS on 443 with funnel allowed: %s", body)
	}
	handlers := config.Web["${TS_CERT_DOMAIN}:443"].Handlers
	if len(handlers) != 3 {
		t.Fatalf("expected one handler per path, got %#v", handlers)
	}
	if handlers["/mcp"].Proxy != funnelProxyTarget+"/mcp" {
		t.Fatalf("mounted paths must be re-added on the target, got %q", handlers["/mcp"].Proxy)
	}
	if handlers["/.well-known/oauth-protected-resource"].Proxy != funnelProxyTarget+"/.well-known/oauth-protected-resource" {
		t.Fatalf("unexpected well-known target: %q", handlers["/.well-known/oauth-protected-resource"].Proxy)
	}
	if handlers["/"].Proxy != funnelProxyTarget {
		t.Fatalf("root mount must proxy to the bare target, got %q", handlers["/"].Proxy)
	}
}

func TestFunnelRevisionTracksConfig(t *testing.T) {
	app := funnelApp()
	first, err := funnelRevision(app)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := funnelRevision(app)
	if first != again || len(first) != 12 {
		t.Fatalf("revision must be stable: %q vs %q", first, again)
	}
	app.Funnel.Paths = []string{"/mcp"}
	changed, _ := funnelRevision(app)
	if changed == first {
		t.Fatal("changing paths must change the revision")
	}
	app.Funnel.Host = "ledger-api"
	renamed, _ := funnelRevision(app)
	if renamed == changed {
		t.Fatal("changing the host must change the revision")
	}
}

func TestFunnelBootPlan(t *testing.T) {
	app := funnelApp()
	revision, err := funnelRevision(app)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		state    funnelContainerState
		want     string
		needsKey bool
	}{
		{"missing", funnelContainerState{}, "first boot", true},
		{"stale", funnelContainerState{exists: true, running: true, revision: "old", backend: "Running"}, "config changed", false},
		{"stopped", funnelContainerState{exists: true, revision: revision}, "container stopped", false},
		{"status unavailable", funnelContainerState{exists: true, running: true, revision: revision}, "tailscale status unavailable", false},
		{"needs login", funnelContainerState{exists: true, running: true, revision: revision, backend: "NeedsLogin"}, "tailscale needslogin", true},
		{"current", funnelContainerState{exists: true, running: true, revision: revision, backend: "Running"}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubFunnelContainerState(t, tc.state)
			got, needsKey, err := funnelBootPlan(app)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want || needsKey != tc.needsKey {
				t.Fatalf("expected %q needsKey=%v, got %q needsKey=%v", tc.want, tc.needsKey, got, needsKey)
			}
		})
	}
}

func TestInspectFunnelContainerParsesDockerOutputAndRetriesStatus(t *testing.T) {
	original := commandOutputFunc
	originalDelay := funnelStatusRetryDelay
	t.Cleanup(func() {
		commandOutputFunc = original
		funnelStatusRetryDelay = originalDelay
	})
	funnelStatusRetryDelay = 0
	execCalls := 0
	commandOutputFunc = func(_ time.Duration, name string, args ...string) (string, error) {
		switch args[0] {
		case "inspect":
			return "true PATH=/usr/bin " + funnelRevisionEnv + "=abc123 TS_HOSTNAME=ledger-mcp", nil
		case "exec":
			execCalls++
			if execCalls < 3 {
				return "", errors.New("tailscaled not ready")
			}
			return `{"BackendState":"Running","Self":{"HostName":"ledger-mcp"}}`, nil
		}
		t.Fatalf("unexpected command %s %v", name, args)
		return "", nil
	}
	state := inspectFunnelContainer(funnelApp())
	if !state.exists || !state.running || state.revision != "abc123" || state.hostname != "ledger-mcp" || state.backend != "Running" {
		t.Fatalf("unexpected state: %#v", state)
	}
	if execCalls != 3 {
		t.Fatalf("expected status retried until it answered, got %d calls", execCalls)
	}
}

func TestMintFunnelAuthKeySendsTaggedSingleUseKey(t *testing.T) {
	var payload tailscaleAuthKeyRequest
	withFakeTailscaleAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/tailnet/-/keys" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"key": "tskey-auth-xyz"})
	})
	key, err := mintFunnelAuthKey("tok", funnelApp())
	if err != nil || key != "tskey-auth-xyz" {
		t.Fatalf("expected key, got %q, %v", key, err)
	}
	create := payload.Capabilities.Devices.Create
	if create.Reusable || create.Ephemeral || !create.Preauthorized || len(create.Tags) != 1 || create.Tags[0] != tailscaleServiceTag {
		t.Fatalf("unexpected key capabilities: %#v", create)
	}
	if payload.ExpirySeconds != 3600 {
		t.Fatalf("expected a one-hour key, got %d", payload.ExpirySeconds)
	}
}

func TestMintFunnelAuthKeyExplainsMissingScope(t *testing.T) {
	withFakeTailscaleAPI(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"calling actor does not have enough permissions"}`, http.StatusForbidden)
	})
	_, err := mintFunnelAuthKey("tok", funnelApp())
	if err == nil || !strings.Contains(err.Error(), "Auth Keys write scope") {
		t.Fatalf("expected scope guidance, got %v", err)
	}
}

func TestPrepareFunnelDeployWritesConfigAndMintsOnlyWhenBooting(t *testing.T) {
	writeTailnetState(t, `,"oauth_client_id":"id","oauth_client_secret":"secret"`)
	root := t.TempDir()
	t.Setenv("SINGLESERVER_FUNNEL_ROOT", root)
	minted := 0
	withFakeTailscaleAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/oauth/token":
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "tok"})
		case "/api/v2/tailnet/-/keys":
			minted++
			_ = json.NewEncoder(w).Encode(map[string]string{"key": "tskey-auth-new"})
		default:
			http.NotFound(w, r)
		}
	})
	app := funnelApp()
	if err := app.Normalize(); err != nil {
		t.Fatal(err)
	}
	revision, _ := funnelRevision(app)

	stubFunnelContainerState(t, funnelContainerState{exists: true, running: true, revision: revision, backend: "Running"})
	env, err := prepareFunnelDeploy(app)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(env, " ") != "SINGLESERVER_FUNNEL_BOOT=0" || minted != 0 {
		t.Fatalf("a current container must not mint a key: %v (minted=%d)", env, minted)
	}
	serve, err := os.ReadFile(filepath.Join(root, "ledger", "config", "serve.json"))
	if err != nil || !strings.Contains(string(serve), `"/mcp"`) {
		t.Fatalf("expected serve.json written: %v\n%s", err, serve)
	}

	stubFunnelContainerState(t, funnelContainerState{exists: true, running: true, revision: "old", backend: "Running"})
	env, err = prepareFunnelDeploy(app)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(env, " ") != "SINGLESERVER_FUNNEL_BOOT=1 SINGLESERVER_FUNNEL_REASON=config changed" || minted != 0 {
		t.Fatalf("a logged-in node must reboot without a new key: %v (minted=%d)", env, minted)
	}

	stubFunnelContainerState(t, funnelContainerState{})
	env, err = prepareFunnelDeploy(app)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, " ")
	if !strings.Contains(joined, "SINGLESERVER_FUNNEL_BOOT=1") || !strings.Contains(joined, "SINGLESERVER_FUNNEL_AUTHKEY=tskey-auth-new") || !strings.Contains(joined, "SINGLESERVER_FUNNEL_REASON=first boot") || minted != 1 {
		t.Fatalf("expected a boot with a fresh key: %v (minted=%d)", env, minted)
	}

	plain := app
	plain.Funnel = nil
	env, err = prepareFunnelDeploy(plain)
	if err != nil || env != nil {
		t.Fatalf("apps without a funnel need no env, got %v, %v", env, err)
	}
}

func TestPrepareFunnelDeployRequiresOAuthClient(t *testing.T) {
	writeTailnetState(t, "")
	t.Setenv("SINGLESERVER_FUNNEL_ROOT", t.TempDir())
	stubFunnelContainerState(t, funnelContainerState{})
	app := funnelApp()
	if err := app.Normalize(); err != nil {
		t.Fatal(err)
	}
	_, err := prepareFunnelDeploy(app)
	if err == nil || !strings.Contains(err.Error(), "OAuth client") {
		t.Fatalf("expected OAuth guidance, got %v", err)
	}
}

func TestTeardownFunnelLogsOutRemovesContainerAndState(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SINGLESERVER_FUNNEL_ROOT", root)
	app := funnelApp()
	if err := writeFunnelConfig(app); err != nil {
		t.Fatal(err)
	}
	stubFunnelContainerState(t, funnelContainerState{exists: true, running: true})
	var commands []string
	original := commandRunFunc
	t.Cleanup(func() { commandRunFunc = original })
	commandRunFunc = func(_ time.Duration, name string, args ...string) error {
		commands = append(commands, name+" "+strings.Join(args, " "))
		return nil
	}

	var out bytes.Buffer
	if err := teardownFunnel(app, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Join(commands, "\n") != "docker exec ledger-funnel tailscale logout\ndocker rm -f ledger-funnel" {
		t.Fatalf("unexpected commands:\n%s", strings.Join(commands, "\n"))
	}
	if _, err := os.Stat(filepath.Join(root, "ledger")); !os.IsNotExist(err) {
		t.Fatalf("expected funnel state removed, got %v", err)
	}
	if !strings.Contains(out.String(), "ledger\tfunnel\tok\tremoved") || !strings.Contains(out.String(), "delete the machine ledger-mcp") {
		t.Fatalf("expected removal guidance:\n%s", out.String())
	}
}

func TestParseAddArgsCollectsFunnelFlags(t *testing.T) {
	var out bytes.Buffer
	opts, err := parseAddArgs([]string{
		"acme/ledger", "--tunnel", "private",
		"--funnel-path", "/mcp", "--funnel-path=/.well-known/oauth-protected-resource",
		"--funnel-host", "ledger-mcp", "--non-interactive",
	}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if !opts.funnelPathsSet || !opts.funnelHostSet || opts.funnelHost != "ledger-mcp" || len(opts.funnelPaths) != 2 {
		t.Fatalf("unexpected funnel options: %#v", opts.appSettings)
	}
	app, entry, err := opts.app()
	if err != nil {
		t.Fatal(err)
	}
	if !app.HasFunnel() || entry.funnel == nil || entry.funnel.Host != "ledger-mcp" {
		t.Fatalf("expected funnel on app and entry: %#v %#v", app.Funnel, entry.funnel)
	}
	updated, err := appendAppToConfigYAML(nil, entry)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updated), "funnel:\n      host: ledger-mcp\n      paths:\n        - /mcp\n        - /.well-known/oauth-protected-resource\n") {
		t.Fatalf("expected funnel block:\n%s", updated)
	}
	command := addEquivalentCommand(opts)
	if !strings.Contains(command, "--funnel-path '/mcp' --funnel-path '/.well-known/oauth-protected-resource' --funnel-host 'ledger-mcp'") {
		t.Fatalf("expected funnel flags in equivalent command: %s", command)
	}
}

func TestAddOptionsRejectFunnelOnPublicApp(t *testing.T) {
	opts := addOptions{repo: "acme/ledger", tunnel: "public"}
	opts.funnelPaths = []string{"/mcp"}
	opts.funnelPathsSet = true
	_, _, err := opts.app()
	if err == nil || !strings.Contains(err.Error(), "requires tunnel: private") {
		t.Fatalf("expected private-only error, got %v", err)
	}
}

func TestCheckFunnelHostRejectsServerAndAppNames(t *testing.T) {
	writeTailnetState(t, "")
	app := funnelApp()
	if err := app.Normalize(); err != nil {
		t.Fatal(err)
	}
	other := AppConfig{Repo: "acme/notes", Tunnel: TunnelPrivate}
	if err := other.Normalize(); err != nil {
		t.Fatal(err)
	}
	if err := checkFunnelHost(app, &Config{Apps: []AppConfig{other}}); err != nil {
		t.Fatalf("distinct names must pass: %v", err)
	}
	app.Funnel.Host = "server"
	if err := checkFunnelHost(app, &Config{}); err == nil || !strings.Contains(err.Error(), "this server's own machine name") {
		t.Fatalf("expected server name rejection, got %v", err)
	}
	app.Funnel.Host = "notes"
	if err := checkFunnelHost(app, &Config{Apps: []AppConfig{other}}); err == nil || !strings.Contains(err.Error(), "already the name of acme/notes") {
		t.Fatalf("expected app name rejection, got %v", err)
	}
}

func TestSplitFunnelPaths(t *testing.T) {
	got := splitFunnelPaths(" /mcp, /hooks  /x,")
	if strings.Join(got, "|") != "/mcp|/hooks|/x" {
		t.Fatalf("unexpected split: %#v", got)
	}
}

func TestCliEditSetsAndClearsFunnel(t *testing.T) {
	dir := writeTailnetState(t, "")
	configPath := filepath.Join(dir, "apps.yml")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	if err := os.WriteFile(configPath, []byte("apps:\n  - repo: acme/ledger\n    tunnel: private\n"), 0600); err != nil {
		t.Fatal(err)
	}
	stubEditPrompt(t, false)
	logger := log.New(io.Discard, "", 0)

	var out bytes.Buffer
	if err := cliEdit([]string{"ledger", "--funnel-path", "/mcp", "--funnel-host", "ledger-mcp", "--no-deploy"}, &out, logger); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !config.Apps[0].HasFunnel() || config.Apps[0].Funnel.Host != "ledger-mcp" || config.Apps[0].Funnel.Paths[0] != "/mcp" {
		t.Fatalf("expected funnel set: %#v", config.Apps[0].Funnel)
	}

	if err := cliEdit([]string{"ledger", "--funnel-path", "/hooks", "--no-deploy"}, &out, logger); err != nil {
		t.Fatal(err)
	}
	config, _ = LoadConfig(configPath)
	if config.Apps[0].Funnel.Host != "ledger-mcp" || strings.Join(config.Apps[0].Funnel.Paths, ",") != "/hooks" {
		t.Fatalf("changing paths must keep the host: %#v", config.Apps[0].Funnel)
	}

	originalTeardown := teardownFunnelFunc
	t.Cleanup(func() { teardownFunnelFunc = originalTeardown })
	tornDown := ""
	teardownFunnelFunc = func(app AppConfig, _ io.Writer) error {
		tornDown = app.FunnelLabel()
		return nil
	}
	if err := cliEdit([]string{"ledger", "--no-funnel", "--no-deploy"}, &out, logger); err != nil {
		t.Fatal(err)
	}
	config, _ = LoadConfig(configPath)
	if config.Apps[0].Funnel != nil {
		t.Fatalf("expected funnel cleared: %#v", config.Apps[0].Funnel)
	}
	if tornDown != "ledger-mcp" {
		t.Fatalf("--no-funnel must tear the node down even without a deploy, got %q", tornDown)
	}

	_, err = parseEditArgs([]string{"ledger", "--no-funnel", "--funnel-path", "/x"}, &out)
	if err == nil || !strings.Contains(err.Error(), "cannot be used together") {
		t.Fatalf("expected conflict error, got %v", err)
	}

	err = cliEdit([]string{"ledger", "--funnel-path", "/mcp", "--funnel-host", "server", "--no-deploy"}, &out, logger)
	if err == nil || !strings.Contains(err.Error(), "this server's own machine name") {
		t.Fatalf("edit must reject the server's machine name as funnel host, got %v", err)
	}
	config, _ = LoadConfig(configPath)
	if config.Apps[0].Funnel != nil {
		t.Fatalf("a rejected edit must not be written: %#v", config.Apps[0].Funnel)
	}
}

func TestRemoveTearsDownFunnel(t *testing.T) {
	for _, configured := range []bool{true, false} {
		dir := writeTailnetState(t, "")
		configPath := filepath.Join(dir, "apps.yml")
		t.Setenv("SINGLESERVER_CONFIG", configPath)
		body := "apps:\n  - repo: acme/ledger\n    tunnel: private\n"
		if configured {
			body += "    funnel:\n      host: ledger-mcp\n      paths: [/mcp]\n"
		}
		if err := os.WriteFile(configPath, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		stubFunnelContainerState(t, funnelContainerState{exists: true, running: true, hostname: "ledger-mcp"})
		removeTearsDownFunnel(t, configured)
	}
}

func removeTearsDownFunnel(t *testing.T, configured bool) {
	t.Helper()
	originalSync := syncAppDomainFunc
	originalStop := stopAppContainersFunc
	originalTeardown := teardownFunnelFunc
	t.Cleanup(func() {
		syncAppDomainFunc = originalSync
		stopAppContainersFunc = originalStop
		teardownFunnelFunc = originalTeardown
	})
	syncAppDomainFunc = func(AppConfig, string, bool, io.Writer) error { return nil }
	stopAppContainersFunc = func(string) error { return nil }
	tornDown := false
	teardownFunnelFunc = func(app AppConfig, _ io.Writer) error {
		tornDown = true
		if configured && app.FunnelLabel() != "ledger-mcp" {
			t.Fatalf("unexpected app passed to teardown: %#v", app.Funnel)
		}
		return nil
	}

	var out bytes.Buffer
	if err := cliRemove([]string{"ledger", "--non-interactive"}, &out); err != nil {
		t.Fatal(err)
	}
	if !tornDown {
		t.Fatalf("expected the funnel torn down (configured=%v)", configured)
	}
}

func TestRemoveKeepsConfigWhenFunnelTeardownFails(t *testing.T) {
	dir := writeTailnetState(t, "")
	configPath := filepath.Join(dir, "apps.yml")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	if err := os.WriteFile(configPath, []byte("apps:\n  - repo: acme/ledger\n    tunnel: private\n    funnel:\n      paths: [/mcp]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	originalSync := syncAppDomainFunc
	originalTeardown := teardownFunnelFunc
	t.Cleanup(func() {
		syncAppDomainFunc = originalSync
		teardownFunnelFunc = originalTeardown
	})
	syncAppDomainFunc = func(AppConfig, string, bool, io.Writer) error { return nil }
	teardownFunnelFunc = func(AppConfig, io.Writer) error { return errors.New("docker down") }

	var out bytes.Buffer
	err := cliRemove([]string{"ledger", "--non-interactive"}, &out)
	if err == nil || !strings.Contains(err.Error(), "config left unchanged") {
		t.Fatalf("expected teardown failure, got %v", err)
	}
	config, err := LoadConfig(configPath)
	if err != nil || len(config.Apps) != 1 {
		t.Fatalf("expected the app kept in config, got %v %v", config, err)
	}
}

func TestDoctorFunnelReportsHealthyNode(t *testing.T) {
	writeTailnetState(t, "")
	app := funnelApp()
	if err := app.Normalize(); err != nil {
		t.Fatal(err)
	}
	revision, _ := funnelRevision(app)
	stubFunnelContainerState(t, funnelContainerState{exists: true, running: true, revision: revision, backend: "Running"})
	originalLookup := funnelLookupHostFunc
	originalReady := funnelReadyFunc
	t.Cleanup(func() {
		funnelLookupHostFunc = originalLookup
		funnelReadyFunc = originalReady
	})
	funnelLookupHostFunc = func(context.Context, string) ([]string, error) { return []string{"203.0.113.10"}, nil }
	funnelReadyFunc = func(string, time.Duration) error { return nil }

	var out bytes.Buffer
	if !doctorFunnel(&out, app) {
		t.Fatalf("expected healthy funnel:\n%s", out.String())
	}
	for _, want := range []string{"ledger\tfunnel_container\tok\tledger-funnel", "ledger\tfunnel_dns\tok\tledger-mcp.corp.ts.net", "ledger\tfunnel_https\tok\thttps://ledger-mcp.corp.ts.net/mcp"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("expected %q in:\n%s", want, out.String())
		}
	}

	stubFunnelContainerState(t, funnelContainerState{})
	out.Reset()
	if doctorFunnel(&out, app) || !strings.Contains(out.String(), "funnel_container\tfailed") {
		t.Fatalf("expected missing container failure:\n%s", out.String())
	}

	stubFunnelContainerState(t, funnelContainerState{exists: true, running: true, revision: revision, backend: "Running"})
	funnelLookupHostFunc = func(context.Context, string) ([]string, error) { return nil, errors.New("NXDOMAIN") }
	funnelReadyFunc = func(string, time.Duration) error { return errors.New("dial tcp: no such host") }
	out.Reset()
	if doctorFunnel(&out, app) {
		t.Fatalf("expected failure when the name is not public:\n%s", out.String())
	}
	for _, want := range []string{"funnel_dns\tfailed", "funnel_https\tfailed", "funnel node attribute"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("expected %q in:\n%s", want, out.String())
		}
	}
}

func TestWriteConfigOmitsDefaultFunnelHost(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "apps.yml")
	t.Setenv("SINGLESERVER_STATE_DIR", dir)
	app := AppConfig{Repo: "acme/ledger", Tunnel: TunnelPrivate, Funnel: &FunnelConfig{Paths: []string{"/mcp"}}}
	if err := writeConfig(path, &Config{Apps: []AppConfig{app}}); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(path)
	if strings.Contains(string(body), "host:") || !strings.Contains(string(body), "paths:\n        - /mcp") {
		t.Fatalf("default host must not be persisted:\n%s", body)
	}
	loaded, err := LoadConfig(path)
	if err != nil || loaded.Apps[0].Funnel.Host != "ledger-public" {
		t.Fatalf("default host must come back on load: %v %#v", err, loaded.Apps[0].Funnel)
	}
}

func TestWriteConfigKeepsPrivateTunnelWithoutOtherSettings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "apps.yml")
	if err := writeConfig(path, &Config{Apps: []AppConfig{{Repo: "acme/ledger", Tunnel: TunnelPrivate}}}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "tunnel: private") {
		t.Fatalf("private tunnel must survive a rewrite:\n%s", body)
	}
}

func TestFunnelContainerIsNotTheAppContainer(t *testing.T) {
	containers := map[string]string{"ledger-funnel": "ledger-funnel", "ledger-web-abc1234": "ledger-web-abc1234"}
	if got := deployedCommitForApp("ledger", containers); got != "abc1234" {
		t.Fatalf("expected the web container's commit, got %q", got)
	}
	if got := matchingAppContainerNames("ledger", "ledger-funnel\nledger-web-abc1234\nother-web-1\n"); len(got) != 1 || got[0] != "ledger-web-abc1234" {
		t.Fatalf("funnel container must not count as an app container: %#v", got)
	}
	if _, ok := containerForApp("ledger", map[string]string{"ledger-funnel": "ledger-funnel"}); ok {
		t.Fatal("an app with only its funnel container running is not running")
	}
}
