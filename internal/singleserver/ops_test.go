package singleserver

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestDomainsAndStorageCommandsUpdateConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps.yml")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	if err := os.WriteFile(configPath, []byte("apps:\n  - dvassallo/fullsend\n"), 0600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := cliDomains([]string{"add", "fullsend", "play.nobrainer.host"}, &out); err != nil {
		t.Fatal(err)
	}
	storagePath := filepath.Join(dir, "storage")
	if err := cliStorage([]string{"enable", "fullsend", "--path", storagePath, "--mount", "/data"}, &out); err != nil {
		t.Fatal(err)
	}

	config, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	app := config.Apps[0]
	if len(app.Hosts) != 1 || app.Hosts[0] != "play.nobrainer.host" {
		t.Fatalf("unexpected hosts: %#v", app.Hosts)
	}
	if app.Healthcheck != "https://play.nobrainer.host/up" {
		t.Fatalf("unexpected healthcheck: %s", app.Healthcheck)
	}
	if app.Storage == nil || app.Storage.Path != storagePath || app.Storage.Mount != "/data" {
		t.Fatalf("unexpected storage: %#v", app.Storage)
	}
}

func TestEnvCommandWritesServerSideEnv(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps.yml")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	t.Setenv("SINGLESERVER_STATE_DIR", dir)
	if err := os.WriteFile(configPath, []byte("apps:\n  - dvassallo/fullsend\n"), 0600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := cliEnv([]string{"set", "fullsend", "DATABASE_URL=sqlite:///storage/app.db"}, &out); err != nil {
		t.Fatal(err)
	}
	values, err := loadAppEnv("fullsend")
	if err != nil {
		t.Fatal(err)
	}
	if values["DATABASE_URL"] != "sqlite:///storage/app.db" {
		t.Fatalf("unexpected DATABASE_URL: %q", values["DATABASE_URL"])
	}
}
