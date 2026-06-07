package singleserver

import (
	"os"
	"path/filepath"
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
