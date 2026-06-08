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
  - dvassallo/sillyface-games
  - repo: dvassallo/fullsend
    branch: master
    healthcheck: https://fullsend.game/up
    hosts:
      - fullsend.game
      - fullsend.assetstacks.com
      - fullsend.game
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
	if config.Apps[0].Name != "sillyface-games" {
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
	config := &Config{Apps: []AppConfig{{Repo: "dvassallo/sillyface-games", Name: "sillyface-games"}}}
	payload := &PushPayload{
		Ref:   "refs/heads/main",
		After: "abc123",
		Repository: Repo{
			FullName:      "dvassallo/sillyface-games",
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
	config := &Config{Apps: []AppConfig{{Repo: "dvassallo/sillyface-games", Name: "sillyface-games"}}}
	payload := &PushPayload{
		Ref:   "refs/heads/feature",
		After: "abc123",
		Repository: Repo{
			FullName: "dvassallo/sillyface-games",
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
	config := &Config{Apps: []AppConfig{{Repo: "dvassallo/fullsend", Name: "fullsend"}}}
	app, ok := config.AppByRepo("DVASSALLO/FULLSEND")
	if !ok {
		t.Fatal("expected app")
	}
	if app.Name != "fullsend" {
		t.Fatalf("unexpected app: %s", app.Name)
	}
}

func TestAppByNameIsCaseInsensitive(t *testing.T) {
	config := &Config{Apps: []AppConfig{{Repo: "dvassallo/fullsend", Name: "fullsend"}}}
	app, ok := config.AppByName("FULLSEND")
	if !ok {
		t.Fatal("expected app")
	}
	if app.Repo != "dvassallo/fullsend" {
		t.Fatalf("unexpected app: %s", app.Repo)
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
		Repo:  "dvassallo/fullsend",
		Hosts: []string{"https://fullsend.game"},
	}
	if err := app.Normalize(); err == nil {
		t.Fatal("expected URL host to be rejected")
	}
}

func TestNormalizeStorageDefaults(t *testing.T) {
	app := AppConfig{
		Repo:    "dvassallo/fullsend",
		Storage: &StorageConfig{},
	}
	if err := app.Normalize(); err != nil {
		t.Fatal(err)
	}
	if app.Storage.Path != "/srv/storage/fullsend" {
		t.Fatalf("unexpected storage path: %s", app.Storage.Path)
	}
	if app.Storage.Mount != "/storage" {
		t.Fatalf("unexpected storage mount: %s", app.Storage.Mount)
	}
}
