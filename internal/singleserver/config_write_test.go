package singleserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteConfigPreservesScalarForRepoOnlyAndWritesOverrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "apps.yml")
	config := &Config{Apps: []AppConfig{
		{Repo: "dvassallo/sillyface-games"},
		{
			Repo:            "dvassallo/fullsend",
			Hosts:           []string{"fullsend.game"},
			Healthcheck:     "https://fullsend.game/up",
			AppPort:         3000,
			HealthcheckPath: "/up",
			Storage:         &StorageConfig{Path: "/srv/storage/fullsend", Mount: "/storage"},
		},
	}}
	for i := range config.Apps {
		if err := config.Apps[i].Normalize(); err != nil {
			t.Fatal(err)
		}
	}

	if err := writeConfig(path, config); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "  - dvassallo/sillyface-games\n") {
		t.Fatalf("expected scalar repo entry:\n%s", text)
	}
	if !strings.Contains(text, "app_port: 3000") {
		t.Fatalf("expected app_port override:\n%s", text)
	}
	if !strings.Contains(text, "storage:\n      path: /srv/storage/fullsend\n      mount: /storage") {
		t.Fatalf("expected storage override:\n%s", text)
	}
}
