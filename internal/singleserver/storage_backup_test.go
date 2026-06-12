package singleserver

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNextBackupPathUsesTimestamp(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 8, 20, 30, 0, 0, time.UTC)

	path, err := nextBackupPath(dir, now)
	if err != nil {
		t.Fatal(err)
	}

	want := filepath.Join(dir, "20260608T203000Z.tar.gz")
	if path != want {
		t.Fatalf("got %s, want %s", path, want)
	}
}

func TestNextBackupPathAvoidsExistingTimestamp(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 8, 20, 30, 0, 0, time.UTC)
	existing := filepath.Join(dir, "20260608T203000Z.tar.gz")
	if err := os.WriteFile(existing, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}

	path, err := nextBackupPath(dir, now)
	if err != nil {
		t.Fatal(err)
	}

	want := filepath.Join(dir, "20260608T203000Z-1.tar.gz")
	if path != want {
		t.Fatalf("got %s, want %s", path, want)
	}
}

func TestResolveBackupPathAcceptsIDOrPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SINGLESERVER_BACKUP_DIR", dir)

	if got := resolveBackupPath("scoreboard", "20260608T203000Z"); got != filepath.Join(dir, "scoreboard", "20260608T203000Z.tar.gz") {
		t.Fatalf("unexpected backup id path: %s", got)
	}
	if got := resolveBackupPath("scoreboard", "backup.tar.gz"); got != "backup.tar.gz" {
		t.Fatalf("expected explicit archive path, got %s", got)
	}
	if got := resolveBackupPath("scoreboard", "./backup.tar.gz"); got != "./backup.tar.gz" {
		t.Fatalf("expected relative archive path, got %s", got)
	}
}
