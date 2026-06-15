package singleserver

import (
	"os"
	"path/filepath"
	"strings"
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

func TestSnapshotStorageExcludesSQLiteSidecars(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	write := func(name string, data []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(src, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("leaderboard.db", append([]byte("SQLite format 3\x00"), make([]byte, 64)...))
	write("leaderboard.db-wal", []byte("wal"))
	write("leaderboard.db-shm", []byte("shm"))
	write("leaderboard.db-journal", []byte("journal"))
	write("config.txt", []byte("plain"))

	// Stub sqlite3 so the test needs no real binary: the -version probe returns
	// ok, and .backup is simulated by copying the source file to the destination.
	originalOutput := commandOutputFunc
	t.Cleanup(func() { commandOutputFunc = originalOutput })
	commandOutputFunc = func(_ time.Duration, name string, args ...string) (string, error) {
		if name == "sqlite3" && len(args) == 2 && strings.HasPrefix(args[1], ".backup ") {
			dest := strings.TrimSuffix(strings.TrimPrefix(args[1], ".backup '"), "'")
			data, err := os.ReadFile(args[0])
			if err != nil {
				return "", err
			}
			return "", os.WriteFile(dest, data, 0600)
		}
		return "", nil
	}

	files, sqliteFiles, err := snapshotStorage(src, dst, &backupProgress{})
	if err != nil {
		t.Fatal(err)
	}
	if files != 2 || sqliteFiles != 1 {
		t.Fatalf("files=%d sqliteFiles=%d, want 2 and 1", files, sqliteFiles)
	}
	for _, name := range []string{"leaderboard.db", "config.txt"} {
		if _, err := os.Stat(filepath.Join(dst, name)); err != nil {
			t.Errorf("expected %s in snapshot: %v", name, err)
		}
	}
	for _, name := range []string{"leaderboard.db-wal", "leaderboard.db-shm", "leaderboard.db-journal"} {
		if _, err := os.Stat(filepath.Join(dst, name)); !os.IsNotExist(err) {
			t.Errorf("expected %s excluded from snapshot, stat err=%v", name, err)
		}
	}
}
