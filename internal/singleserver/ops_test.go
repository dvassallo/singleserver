package singleserver

import (
	"bytes"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
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
	logger := log.New(io.Discard, "", 0)
	if err := cliDomains([]string{"add", "fullsend", "play.nobrainer.host", "--no-deploy"}, &out, logger); err != nil {
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

func TestDomainsRemoveSupportsNoDeployFlagAfterDomain(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps.yml")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	if err := os.WriteFile(configPath, []byte(`apps:
  - repo: dvassallo/fullsend
    hosts:
      - play.nobrainer.host
    healthcheck: https://play.nobrainer.host/up
`), 0600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	logger := log.New(io.Discard, "", 0)
	if err := cliDomains([]string{"remove", "fullsend", "play.nobrainer.host", "--no-deploy"}, &out, logger); err != nil {
		t.Fatal(err)
	}

	config, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Apps[0].Hosts) != 0 {
		t.Fatalf("expected host removed, got %#v", config.Apps[0].Hosts)
	}
	if config.Apps[0].Healthcheck != "" {
		t.Fatalf("expected removed default healthcheck to be cleared, got %s", config.Apps[0].Healthcheck)
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

func TestBackupAndRestoreStorageReplacesDirectory(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps.yml")
	storagePath := filepath.Join(dir, "storage")
	backupRoot := filepath.Join(dir, "backups")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	t.Setenv("SINGLESERVER_BACKUP_DIR", backupRoot)
	if err := os.MkdirAll(filepath.Join(storagePath, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storagePath, "data.txt"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storagePath, "nested", "keep.txt"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`apps:
  - repo: dvassallo/fullsend
    storage:
      path: `+storagePath+`
      mount: /storage
`), 0600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := cliBackup([]string{"fullsend"}, &out); err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(out.String())
	if len(fields) < 4 {
		t.Fatalf("unexpected backup output: %q", out.String())
	}
	backupPath := fields[3]

	if err := os.WriteFile(filepath.Join(storagePath, "data.txt"), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storagePath, "extra.txt"), []byte("extra"), 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := cliRestore([]string{"fullsend", backupPath, "--yes", "--no-restart"}, &out); err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(filepath.Join(storagePath, "data.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "old" {
		t.Fatalf("expected restored content, got %q", string(body))
	}
	if _, err := os.Stat(filepath.Join(storagePath, "extra.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected extra file removed, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(storagePath, "nested", "keep.txt")); err != nil {
		t.Fatalf("expected nested file restored: %v", err)
	}
}

func TestRestoreRequiresConfirmation(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps.yml")
	storagePath := filepath.Join(dir, "storage")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	if err := os.MkdirAll(storagePath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`apps:
  - repo: dvassallo/fullsend
    storage:
      path: `+storagePath+`
      mount: /storage
`), 0600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err := cliRestore([]string{"fullsend", filepath.Join(dir, "missing.tar.gz"), "--no-restart"}, &out)
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("expected --yes confirmation error, got %v", err)
	}
}

func TestRemoveDeleteStorageRequiresConfirmation(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps.yml")
	storagePath := filepath.Join(dir, "storage")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	t.Setenv("SINGLESERVER_STATE_DIR", dir)
	if err := os.MkdirAll(storagePath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`apps:
  - repo: dvassallo/fullsend
    storage:
      path: `+storagePath+`
      mount: /storage
`), 0600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err := cliRemove([]string{"fullsend", "--delete-storage"}, &out)
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("expected --yes confirmation error, got %v", err)
	}
	if _, err := os.Stat(storagePath); err != nil {
		t.Fatalf("expected storage kept: %v", err)
	}
	config, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Apps) != 1 {
		t.Fatalf("expected app config kept, got %#v", config.Apps)
	}
}

func TestRemoveDeleteStorageWithConfirmation(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps.yml")
	storagePath := filepath.Join(dir, "storage")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	t.Setenv("SINGLESERVER_STATE_DIR", dir)
	if err := os.MkdirAll(storagePath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storagePath, "data.txt"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`apps:
  - repo: dvassallo/fullsend
    storage:
      path: `+storagePath+`
      mount: /storage
`), 0600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := cliRemove([]string{"fullsend", "--delete-storage", "--yes"}, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(storagePath); !os.IsNotExist(err) {
		t.Fatalf("expected storage deleted, stat err=%v", err)
	}
	config, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Apps) != 0 {
		t.Fatalf("expected app config removed, got %#v", config.Apps)
	}
}
