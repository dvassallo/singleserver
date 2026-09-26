package singleserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadConfigSupportsStringAndMapApps(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "apps.yml")
	body := []byte(`apps:
  - acme/arcade-games
  - repo: acme/scoreboard
    branch: master
    healthcheck: https://scoreboard.example.com/up
    hosts:
      - scoreboard.example.com
      - scoreboard-alt.example.com
      - scoreboard.example.com
    app_port: 3000
    healthcheck_path: health
`)
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}

	config, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Apps) != 2 {
		t.Fatalf("expected 2 apps, got %d", len(config.Apps))
	}
	if config.Apps[0].Name != "arcade-games" {
		t.Fatalf("unexpected default app name: %s", config.Apps[0].Name)
	}
	if config.Apps[1].Branch != "master" {
		t.Fatalf("unexpected branch override: %s", config.Apps[1].Branch)
	}
	if config.Apps[1].AppPort != 3000 {
		t.Fatalf("unexpected app_port: %d", config.Apps[1].AppPort)
	}
	if config.Apps[1].HealthcheckPath != "/health" {
		t.Fatalf("unexpected healthcheck_path: %s", config.Apps[1].HealthcheckPath)
	}
	if got := len(config.Apps[1].Hosts); got != 2 {
		t.Fatalf("expected duplicate hosts to be removed, got %d", got)
	}
}

func TestAppForPushUsesDefaultBranch(t *testing.T) {
	config := &Config{Apps: []AppConfig{{Repo: "acme/arcade-games", Name: "arcade-games"}}}
	payload := &PushPayload{
		Ref:   "refs/heads/main",
		After: "abc123",
		Repository: Repo{
			FullName:      "acme/arcade-games",
			DefaultBranch: "main",
		},
	}

	app, branch, reason := config.AppForPush(payload)
	if app == nil {
		t.Fatalf("expected app, got reason %q", reason)
	}
	if branch != "main" {
		t.Fatalf("unexpected branch: %s", branch)
	}
}

func TestAppForPushRequiresDefaultBranchWhenAppBranchUnset(t *testing.T) {
	config := &Config{Apps: []AppConfig{{Repo: "acme/arcade-games", Name: "arcade-games"}}}
	payload := &PushPayload{
		Ref:   "refs/heads/feature",
		After: "abc123",
		Repository: Repo{
			FullName: "acme/arcade-games",
		},
	}

	app, branch, reason := config.AppForPush(payload)
	if app != nil {
		t.Fatalf("did not expect app for branch %s", branch)
	}
	if branch != "feature" {
		t.Fatalf("unexpected branch: %s", branch)
	}
	if !strings.Contains(reason, "default branch is missing") {
		t.Fatalf("unexpected reason: %s", reason)
	}
}

func TestAppByRepoIsCaseInsensitive(t *testing.T) {
	config := &Config{Apps: []AppConfig{{Repo: "acme/scoreboard", Name: "scoreboard"}}}
	app, ok := config.AppByRepo("ACME/SCOREBOARD")
	if !ok {
		t.Fatal("expected app")
	}
	if app.Name != "scoreboard" {
		t.Fatalf("unexpected app: %s", app.Name)
	}
}

func TestAppByNameIsCaseInsensitive(t *testing.T) {
	config := &Config{Apps: []AppConfig{{Repo: "acme/scoreboard", Name: "scoreboard"}}}
	app, ok := config.AppByName("SCOREBOARD")
	if !ok {
		t.Fatal("expected app")
	}
	if app.Repo != "acme/scoreboard" {
		t.Fatalf("unexpected app: %s", app.Repo)
	}
}

func TestAppByNameOrRepoMatchesEitherIdentifier(t *testing.T) {
	config := &Config{Apps: []AppConfig{{Repo: "acme/scoreboard", Name: "scoreboard"}}}

	byName, ok := config.AppByNameOrRepo("SCOREBOARD")
	if !ok {
		t.Fatal("expected app by name")
	}
	if byName.Repo != "acme/scoreboard" {
		t.Fatalf("unexpected app by name: %s", byName.Repo)
	}

	byRepo, ok := config.AppByNameOrRepo("ACME/SCOREBOARD")
	if !ok {
		t.Fatal("expected app by repo")
	}
	if byRepo.Name != "scoreboard" {
		t.Fatalf("unexpected app by repo: %s", byRepo.Name)
	}
}

func TestLoadConfigRejectsDuplicateAppNames(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "apps.yml")
	body := []byte(`apps:
  - alice/homepage
  - bob/homepage
`)
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}

	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected duplicate app name error")
	}
	if !strings.Contains(err.Error(), "duplicate app name in config") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadConfigRejectsDuplicateHostsAcrossApps(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "apps.yml")
	body := []byte(`apps:
  - repo: alice/homepage
    hosts:
      - play.example.com
  - repo: bob/game
    hosts:
      - PLAY.example.com
`)
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}

	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected duplicate host error")
	}
	if !strings.Contains(err.Error(), "duplicate host in config") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNormalizeRejectsURLHosts(t *testing.T) {
	app := AppConfig{
		Repo:  "acme/scoreboard",
		Hosts: []string{"https://scoreboard.example.com"},
	}
	if err := app.Normalize(); err == nil {
		t.Fatal("expected URL host to be rejected")
	}
}

func TestNormalizeStorageDefaults(t *testing.T) {
	app := AppConfig{
		Repo:    "acme/scoreboard",
		Storage: &StorageConfig{},
	}
	if err := app.Normalize(); err != nil {
		t.Fatal(err)
	}
	if app.Storage.Path != "/srv/storage/scoreboard" {
		t.Fatalf("unexpected storage path: %s", app.Storage.Path)
	}
	if app.Storage.Mount != "/storage" {
		t.Fatalf("unexpected storage mount: %s", app.Storage.Mount)
	}
}

func TestNormalizeDefaultsReadinessToRootForRepoDockerfiles(t *testing.T) {
	app := AppConfig{Repo: "acme/scoreboard"}
	if err := app.Normalize(); err != nil {
		t.Fatal(err)
	}
	if app.HealthcheckPath != "/" {
		t.Fatalf("unexpected healthcheck path: %s", app.HealthcheckPath)
	}
}

func TestNormalizeStaticRuntimeDefaults(t *testing.T) {
	app := AppConfig{
		Repo:    "acme/homepage",
		Runtime: "static",
	}
	if err := app.Normalize(); err != nil {
		t.Fatal(err)
	}
	if app.StaticDir != "." {
		t.Fatalf("unexpected static dir: %s", app.StaticDir)
	}
	if app.HealthcheckPath != "/up" {
		t.Fatalf("unexpected healthcheck path: %s", app.HealthcheckPath)
	}
	if app.AppPort != 80 {
		t.Fatalf("unexpected app port: %d", app.AppPort)
	}
}

func TestNormalizeNodeDynamicRequiresStartAndAppPort(t *testing.T) {
	app := AppConfig{
		Repo:    "acme/api",
		Runtime: "node",
	}
	if err := app.Normalize(); err == nil || !strings.Contains(err.Error(), "requires start") {
		t.Fatalf("expected start error, got %v", err)
	}

	app.StartCommand = "npm start"
	if err := app.Normalize(); err == nil || !strings.Contains(err.Error(), "requires app_port") {
		t.Fatalf("expected app_port error, got %v", err)
	}

	app.AppPort = 3000
	app.AppPortSet = true
	if err := app.Normalize(); err != nil {
		t.Fatal(err)
	}
}

func TestNormalizeNodeStaticBuildDoesNotNeedStart(t *testing.T) {
	app := AppConfig{
		Repo:           "acme/homepage",
		Runtime:        "node",
		InstallCommand: "npm ci",
		BuildCommand:   "npm run build",
		StaticDir:      "dist",
	}
	if err := app.Normalize(); err != nil {
		t.Fatal(err)
	}
	if app.AppPort != 80 {
		t.Fatalf("unexpected app port: %d", app.AppPort)
	}
}

func TestNormalizeRejectsUnsafeStaticDir(t *testing.T) {
	app := AppConfig{
		Repo:      "acme/homepage",
		Runtime:   "static",
		StaticDir: "../dist",
	}
	if err := app.Normalize(); err == nil {
		t.Fatal("expected unsafe static_dir error")
	}
}

func TestLoadConfigTracksExplicitAppPortForGeneratedRuntime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "apps.yml")
	body := []byte(`apps:
  - repo: acme/api
    runtime: node
    start: npm start
    app_port: 3000
`)
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !config.Apps[0].AppPortSet {
		t.Fatal("expected explicit app_port to be tracked")
	}
}

func TestDeployTimeoutDurationDefaultsAndOverrides(t *testing.T) {
	tests := []struct {
		name string
		app  AppConfig
		want time.Duration
	}{
		{name: "default", app: AppConfig{Repo: "acme/scoreboard"}, want: defaultDeployTimeout},
		{name: "custom", app: AppConfig{Repo: "acme/scoreboard", DeployTimeout: "45s"}, want: 45 * time.Second},
		{name: "invalid falls back", app: AppConfig{Repo: "acme/scoreboard", DeployTimeout: "not-a-duration"}, want: defaultDeployTimeout},
		{name: "zero falls back", app: AppConfig{Repo: "acme/scoreboard", DeployTimeout: "0s"}, want: defaultDeployTimeout},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.app.DeployTimeoutDuration(); got != test.want {
				t.Fatalf("DeployTimeoutDuration() = %s, want %s", got, test.want)
			}
		})
	}
}

func TestNormalizeRejectsInvalidDeployTimeout(t *testing.T) {
	tests := []struct {
		name    string
		timeout string
		want    string
	}{
		{name: "invalid", timeout: "eventually", want: "invalid deploy_timeout"},
		{name: "zero", timeout: "0s", want: "must be positive"},
		{name: "negative", timeout: "-1s", want: "must be positive"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app := AppConfig{Repo: "acme/scoreboard", DeployTimeout: test.timeout}
			err := app.Normalize()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q error, got %v", test.want, err)
			}
		})
	}
}

func TestStopTimeoutSecondsDefaultsAndOverrides(t *testing.T) {
	tests := []struct {
		name string
		app  AppConfig
		want int
	}{
		{name: "default", app: AppConfig{Repo: "acme/scoreboard"}, want: defaultStopTimeout},
		{name: "custom", app: AppConfig{Repo: "acme/scoreboard", StopTimeout: "10"}, want: 10},
		{name: "invalid falls back", app: AppConfig{Repo: "acme/scoreboard", StopTimeout: "soon"}, want: defaultStopTimeout},
		{name: "zero falls back", app: AppConfig{Repo: "acme/scoreboard", StopTimeout: "0"}, want: defaultStopTimeout},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.app.StopTimeoutSeconds(); got != test.want {
				t.Fatalf("StopTimeoutSeconds() = %d, want %d", got, test.want)
			}
		})
	}
}

func TestDrainTimeoutSecondsDefaultsAndOverrides(t *testing.T) {
	tests := []struct {
		name string
		app  AppConfig
		want int
	}{
		{name: "default", app: AppConfig{Repo: "acme/scoreboard"}, want: defaultDrainTimeout},
		{name: "custom", app: AppConfig{Repo: "acme/scoreboard", DrainTimeout: "10"}, want: 10},
		{name: "invalid falls back", app: AppConfig{Repo: "acme/scoreboard", DrainTimeout: "soon"}, want: defaultDrainTimeout},
		{name: "zero falls back", app: AppConfig{Repo: "acme/scoreboard", DrainTimeout: "0"}, want: defaultDrainTimeout},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.app.DrainTimeoutSeconds(); got != test.want {
				t.Fatalf("DrainTimeoutSeconds() = %d, want %d", got, test.want)
			}
		})
	}
}

func TestNormalizeRejectsInvalidStopTimeout(t *testing.T) {
	tests := []struct {
		name    string
		timeout string
		want    string
	}{
		{name: "invalid", timeout: "soon", want: "invalid stop_timeout"},
		{name: "duration suffix", timeout: "10s", want: "invalid stop_timeout"},
		{name: "zero", timeout: "0", want: "must be positive"},
		{name: "negative", timeout: "-1", want: "must be positive"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app := AppConfig{Repo: "acme/scoreboard", StopTimeout: test.timeout}
			err := app.Normalize()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q error, got %v", test.want, err)
			}
		})
	}
}

func TestNormalizeRejectsInvalidDrainTimeout(t *testing.T) {
	tests := []struct {
		name    string
		timeout string
		want    string
	}{
		{name: "invalid", timeout: "soon", want: "invalid drain_timeout"},
		{name: "duration suffix", timeout: "10s", want: "invalid drain_timeout"},
		{name: "zero", timeout: "0", want: "must be positive"},
		{name: "negative", timeout: "-1", want: "must be positive"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app := AppConfig{Repo: "acme/scoreboard", DrainTimeout: test.timeout}
			err := app.Normalize()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q error, got %v", test.want, err)
			}
		})
	}
}

func TestLoadConfigParsesStopAndDrainTimeout(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "apps.yml")
	body := []byte(`apps:
  - repo: acme/scoreboard
    stop_timeout: 10
    drain_timeout: 5
`)
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	app := config.Apps[0]
	if app.StopTimeout != "10" {
		t.Fatalf("unexpected stop_timeout: %q", app.StopTimeout)
	}
	if app.DrainTimeout != "5" {
		t.Fatalf("unexpected drain_timeout: %q", app.DrainTimeout)
	}
	if app.StopTimeoutSeconds() != 10 {
		t.Fatalf("unexpected StopTimeoutSeconds: %d", app.StopTimeoutSeconds())
	}
	if app.DrainTimeoutSeconds() != 5 {
		t.Fatalf("unexpected DrainTimeoutSeconds: %d", app.DrainTimeoutSeconds())
	}
}

func TestLoadConfigRejectsPrivateAppsSharingAServiceName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "apps.yml")
	body := []byte(`apps:
  - repo: alice/homepage
    tunnel: private
    hosts:
      - notes.alice.ts.net
  - repo: bob/notes
    tunnel: private
    hosts:
      - notes.bob.ts.net
`)
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}

	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected duplicate tailscale service error")
	}
	if !strings.Contains(err.Error(), "duplicate tailscale service in config") || !strings.Contains(err.Error(), "svc:notes") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadConfigAllowsPrivateAppsWithDistinctServiceNames(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "apps.yml")
	body := []byte(`apps:
  - repo: alice/homepage
    tunnel: private
    hosts:
      - notes.alice.ts.net
  - repo: bob/game
    tunnel: private
    hosts:
      - scores.bob.ts.net
`)
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadConfig(path); err != nil {
		t.Fatal(err)
	}
}

func TestNormalizeRejectsUnknownTunnel(t *testing.T) {
	app := AppConfig{Repo: "acme/scoreboard", Tunnel: "vpn"}
	err := app.Normalize()
	if err == nil {
		t.Fatal("expected unknown tunnel to be rejected")
	}
	if !strings.Contains(err.Error(), "invalid tunnel") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNormalizeInfersTunnelFromHosts(t *testing.T) {
	cases := []struct {
		name string
		app  AppConfig
		want Tunnel
	}{
		{"tailnet host", AppConfig{Repo: "acme/scoreboard", Hosts: []string{"scores.corp.ts.net"}}, TunnelPrivate},
		{"public host", AppConfig{Repo: "acme/scoreboard", Hosts: []string{"scores.example.com"}}, TunnelPublic},
		{"no host", AppConfig{Repo: "acme/scoreboard"}, TunnelPublic},
		{"explicit wins over host", AppConfig{Repo: "acme/scoreboard", Tunnel: " PUBLIC ", Hosts: []string{"scores.corp.ts.net"}}, TunnelPublic},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := tc.app
			if err := app.Normalize(); err != nil {
				t.Fatal(err)
			}
			if app.Tunnel != tc.want {
				t.Fatalf("got %q, want %q", app.Tunnel, tc.want)
			}
		})
	}
}

func TestNormalizeRejectsPublicDomainOnPrivateApp(t *testing.T) {
	app := AppConfig{Repo: "acme/scoreboard", Tunnel: TunnelPrivate, Hosts: []string{"scores.example.com"}}
	err := app.Normalize()
	if err == nil {
		t.Fatal("expected a public domain on a private app to be rejected")
	}
	if !strings.Contains(err.Error(), ".ts.net") {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, host := range []string{"scores", "scores.corp.ts.net"} {
		app := AppConfig{Repo: "acme/scoreboard", Tunnel: TunnelPrivate, Hosts: []string{host}}
		if err := app.Normalize(); err != nil {
			t.Fatalf("%s should be a valid private host: %v", host, err)
		}
	}
}

func TestNormalizeCapsPrivateAppsAfterDeduplicatingHosts(t *testing.T) {
	app := AppConfig{Repo: "acme/scoreboard", Tunnel: TunnelPrivate, Hosts: []string{"scores.corp.ts.net", "scores.corp.ts.net"}}
	if err := app.Normalize(); err != nil {
		t.Fatalf("duplicate hosts collapse to one domain, so this is not over the cap: %v", err)
	}
	if len(app.Hosts) != 1 {
		t.Fatalf("unexpected hosts: %#v", app.Hosts)
	}

	app = AppConfig{Repo: "acme/scoreboard", Tunnel: TunnelPrivate, Hosts: []string{"scores.corp.ts.net", "notes.corp.ts.net"}}
	if err := app.Normalize(); err == nil {
		t.Fatal("expected two distinct domains to be rejected")
	}
}

func TestQualifiedHostsExpandBareLabelsForPrivateAppsOnly(t *testing.T) {
	t.Setenv("SINGLESERVER_STATE_DIR", t.TempDir())
	if err := writeTailscaleState(&TailscaleState{Hostname: "box.corp.ts.net"}); err != nil {
		t.Fatal(err)
	}

	private := AppConfig{Name: "scoreboard", Tunnel: TunnelPrivate, Hosts: []string{"scores"}}
	if got := private.QualifiedHosts(); len(got) != 1 || got[0] != "scores.corp.ts.net" {
		t.Fatalf("expected the bare label expanded, got %#v", got)
	}

	qualified := AppConfig{Name: "scoreboard", Tunnel: TunnelPrivate, Hosts: []string{"scores.corp.ts.net"}}
	if got := qualified.QualifiedHosts(); got[0] != "scores.corp.ts.net" {
		t.Fatalf("expected an already-qualified host left alone, got %#v", got)
	}

	public := AppConfig{Name: "scoreboard", Tunnel: TunnelPublic, Hosts: []string{"scores"}}
	if got := public.QualifiedHosts(); got[0] != "scores" {
		t.Fatalf("expected a public host left alone, got %#v", got)
	}
}
