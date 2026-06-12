package singleserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
