package singleserver

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBackupAndRestoreStorageReplacesDirectory(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps.yml")
	storagePath := filepath.Join(dir, "storage")
	backupRoot := filepath.Join(dir, "backups")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	t.Setenv("SINGLESERVER_BACKUP_DIR", backupRoot)
	stubCommandRun(t)
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
  - repo: acme/scoreboard
    storage:
      path: `+storagePath+`
      mount: /storage
`), 0600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := cliBackup([]string{"scoreboard"}, &out); err != nil {
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
	if err := cliRestore([]string{"scoreboard", backupPath, "--non-interactive", "--no-restart"}, &out); err != nil {
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

func TestRestoreFailsBeforeReplacingStorageWhenOwnershipFixFails(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps.yml")
	storagePath := filepath.Join(dir, "storage")
	backupRoot := filepath.Join(dir, "backups")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	t.Setenv("SINGLESERVER_BACKUP_DIR", backupRoot)
	if err := os.MkdirAll(storagePath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storagePath, "data.txt"), []byte("backup"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`apps:
  - repo: acme/scoreboard
    storage:
      path: `+storagePath+`
      mount: /storage
`), 0600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := cliBackup([]string{"scoreboard"}, &out); err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(out.String())
	if len(fields) < 4 {
		t.Fatalf("unexpected backup output: %q", out.String())
	}
	backupPath := fields[3]
	if err := os.WriteFile(filepath.Join(storagePath, "data.txt"), []byte("current"), 0600); err != nil {
		t.Fatal(err)
	}

	originalRun := commandRunFunc
	t.Cleanup(func() { commandRunFunc = originalRun })
	commandRunFunc = func(timeout time.Duration, name string, args ...string) error {
		return errors.New("chown failed")
	}
	out.Reset()
	err := cliRestore([]string{"scoreboard", backupPath, "--non-interactive", "--no-restart"}, &out)
	if err == nil {
		t.Fatal("expected chown error")
	}
	if !strings.Contains(err.Error(), "chown ") {
		t.Fatalf("unexpected error: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(storagePath, "data.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "current" {
		t.Fatalf("expected current storage to remain untouched, got %q", string(body))
	}
	if strings.Contains(out.String(), "restore\tok") {
		t.Fatalf("unexpected restore success output: %s", out.String())
	}
}

func TestRestoreRejectsRemovedYesFlag(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps.yml")
	storagePath := filepath.Join(dir, "storage")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	if err := os.MkdirAll(storagePath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`apps:
  - repo: acme/scoreboard
    storage:
      path: `+storagePath+`
      mount: /storage
`), 0600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err := cliRestore([]string{"scoreboard", filepath.Join(dir, "missing.tar.gz"), "--yes", "--no-restart"}, &out)
	if err == nil || !strings.Contains(err.Error(), "--yes has been removed") {
		t.Fatalf("expected removed --yes error, got %v", err)
	}
}
