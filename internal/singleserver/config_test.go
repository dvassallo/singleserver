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

func TestAppIndexMatchesEitherIdentifier(t *testing.T) {
	config := &Config{Apps: []AppConfig{{Repo: "acme/scoreboard", Name: "scoreboard"}}}

	byName, err := config.AppIndex("SCOREBOARD")
	if err != nil || byName != 0 {
		t.Fatalf("expected app by name, got %d, %v", byName, err)
	}

	byRepo, err := config.AppIndex("ACME/SCOREBOARD")
	if err != nil || byRepo != 0 {
		t.Fatalf("expected app by repo, got %d, %v", byRepo, err)
	}
}

func TestAppIndexPrefersName(t *testing.T) {
	index, err := twoBranchConfig().AppIndex("scoreboard-prod")
	if err != nil || index != 1 {
		t.Fatalf("expected scoreboard-prod, got %d, %v", index, err)
	}
}

func TestAppIndexRejectsAmbiguousRepo(t *testing.T) {
	_, err := twoBranchConfig().AppIndex("acme/scoreboard")
	if err == nil || err.Error() != "acme/scoreboard has more than one app (scoreboard-dev, scoreboard-prod); use the app name" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAppIndexReportsMissingApp(t *testing.T) {
	_, err := twoBranchConfig().AppIndex("nope")
	if err == nil || err.Error() != "nope is not configured" {
		t.Fatalf("unexpected error: %v", err)
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

func loadConfigBody(t *testing.T, body string) (*Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "apps.yml")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return LoadConfig(path)
}

func TestLoadConfigAllowsSameRepoOnDifferentBranches(t *testing.T) {
	config, err := loadConfigBody(t, `apps:
  - repo: acme/scoreboard
    name: scoreboard-dev
    branch: main
  - repo: acme/scoreboard
    name: scoreboard-prod
    branch: production
`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(config.Apps) != 2 {
		t.Fatalf("expected 2 apps, got %d", len(config.Apps))
	}
}

func TestLoadConfigRejectsDuplicateRepoAndBranch(t *testing.T) {
	_, err := loadConfigBody(t, `apps:
  - repo: acme/scoreboard
    name: scoreboard-dev
    branch: main
  - repo: acme/scoreboard
    name: scoreboard-prod
    branch: main
`)
	if err == nil || !strings.Contains(err.Error(), "duplicate repo and branch in config: acme/scoreboard@main is used by scoreboard-dev and scoreboard-prod") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadConfigRejectsDuplicateRepoWithoutBranches(t *testing.T) {
	_, err := loadConfigBody(t, `apps:
  - repo: acme/scoreboard
    name: scoreboard-dev
  - repo: acme/scoreboard
    name: scoreboard-prod
`)
	if err == nil || !strings.Contains(err.Error(), "duplicate repo in config: acme/scoreboard") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadConfigRequiresBranchWhenRepoIsShared(t *testing.T) {
	_, err := loadConfigBody(t, `apps:
  - repo: acme/scoreboard
    name: scoreboard-dev
  - repo: acme/scoreboard
    name: scoreboard-prod
    branch: production
`)
	if err == nil || !strings.Contains(err.Error(), "acme/scoreboard has more than one app, so app scoreboard-dev must set a branch") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadConfigComparesSharedReposWithoutCase(t *testing.T) {
	_, err := loadConfigBody(t, `apps:
  - repo: Acme/Scoreboard
    name: scoreboard-dev
    branch: main
  - repo: acme/scoreboard
    name: scoreboard-prod
    branch: main
`)
	if err == nil || !strings.Contains(err.Error(), "duplicate repo and branch in config") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadConfigKeepsSingleAppReposWithoutBranch(t *testing.T) {
	config, err := loadConfigBody(t, `apps:
  - acme/arcade-games
  - acme/scoreboard
`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if config.Apps[0].Branch != "" || config.Apps[1].Branch != "" {
		t.Fatalf("expected no branches, got %q and %q", config.Apps[0].Branch, config.Apps[1].Branch)
	}
}

func TestLoadConfigNamesAppsForDuplicateHostInSharedRepo(t *testing.T) {
	_, err := loadConfigBody(t, `apps:
  - repo: acme/scoreboard
    name: scoreboard-dev
    branch: main
    hosts:
      - dev.example.com
  - repo: acme/scoreboard
    name: scoreboard-prod
    branch: production
    hosts:
      - dev.example.com
`)
	if err == nil {
		t.Fatal("expected duplicate host error")
	}
	want := "duplicate host in config: dev.example.com is used by scoreboard-dev and scoreboard-prod"
	if err.Error() != want {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadConfigAsksForNameInSharedRepo(t *testing.T) {
	_, err := loadConfigBody(t, `apps:
  - repo: acme/scoreboard
    branch: main
  - repo: acme/scoreboard
    branch: production
`)
	if err == nil {
		t.Fatal("expected duplicate app name error")
	}
	want := "duplicate app name in config: scoreboard is used twice by acme/scoreboard; give each app of this repo its own name"
	if err.Error() != want {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAppsMatchingReturnsEveryMatch(t *testing.T) {
	apps := twoBranchConfig().Apps

	byRepo := appsMatching(apps, "acme/scoreboard")
	if len(byRepo) != 2 || byRepo[0].Name != "scoreboard-dev" || byRepo[1].Name != "scoreboard-prod" {
		t.Fatalf("expected both apps by repo, got %#v", byRepo)
	}

	byName := appsMatching(apps, "scoreboard-prod")
	if len(byName) != 1 || byName[0].Name != "scoreboard-prod" {
		t.Fatalf("expected exactly scoreboard-prod, got %#v", byName)
	}

	if got := appsMatching(apps, "nope"); got != nil {
		t.Fatalf("expected nil for no match, got %#v", got)
	}
}

func twoBranchConfig() *Config {
	return &Config{Apps: []AppConfig{
		{Repo: "acme/scoreboard", Name: "scoreboard-dev", Branch: "main"},
		{Repo: "acme/scoreboard", Name: "scoreboard-prod", Branch: "production"},
	}}
}

func pushTo(branch string) *PushPayload {
	return &PushPayload{
		Ref:   "refs/heads/" + branch,
		After: "abc123",
		Repository: Repo{
			FullName:      "acme/scoreboard",
			DefaultBranch: "main",
		},
	}
}

func TestAppForPushPicksAppByBranch(t *testing.T) {
	config := twoBranchConfig()

	app, _, reason := config.AppForPush(pushTo("production"))
	if app == nil || app.Name != "scoreboard-prod" {
		t.Fatalf("expected scoreboard-prod, got %v (%s)", app, reason)
	}

	app, _, reason = config.AppForPush(pushTo("main"))
	if app == nil || app.Name != "scoreboard-dev" {
		t.Fatalf("expected scoreboard-dev, got %v (%s)", app, reason)
	}
}

func TestAppForPushIgnoresBranchWithoutApp(t *testing.T) {
	app, branch, reason := twoBranchConfig().AppForPush(pushTo("feature"))
	if app != nil {
		t.Fatalf("did not expect app %s", app.Name)
	}
	if branch != "feature" {
		t.Fatalf("unexpected branch: %s", branch)
	}
	if !strings.Contains(reason, "does not match") {
		t.Fatalf("unexpected reason: %s", reason)
	}
}
