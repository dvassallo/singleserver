package singleserver

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestBackupProgressEnabledOnlyForTerminalOutput(t *testing.T) {
	if newBackupProgress(&bytes.Buffer{}).enabled {
		t.Fatal("expected disabled for a plain writer")
	}
	if newBackupProgress(newTextOutput(&bytes.Buffer{})).enabled {
		t.Fatal("expected disabled for a non-terminal Output")
	}
	if newBackupProgress(newJSONOutput(&bytes.Buffer{})).enabled {
		t.Fatal("expected disabled for JSON output")
	}
}

func TestBackupProgressDisabledWritesNothing(t *testing.T) {
	var buf bytes.Buffer
	p := &backupProgress{w: &buf, enabled: false}
	p.phase("x", 10)
	p.add(5)
	p.set(7)
	p.finish()
	if buf.Len() != 0 {
		t.Fatalf("disabled progress wrote %q", buf.String())
	}
}

func TestBackupProgressRendersBarAndClears(t *testing.T) {
	var buf bytes.Buffer
	p := &backupProgress{w: &buf, enabled: true}
	p.phase("snapshot db", 100)                 // forced render at 0%
	p.lastRender = time.Now().Add(-time.Second) // bypass the throttle
	p.add(100)                                  // render at 100%
	p.finish()                                  // erase the line

	got := buf.String()
	for _, want := range []string{"snapshot db", "100%", "\r"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected output to contain %q, got %q", want, got)
		}
	}
	if !strings.HasSuffix(got, "\r\x1b[K") {
		t.Fatalf("expected trailing clear sequence, got %q", got)
	}
}

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
		{3939295232, "3.7 GB"},
	}
	for _, c := range cases {
		if got := humanBytes(c.n); got != c.want {
			t.Errorf("humanBytes(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestSqliteBackupTimeoutScalesWithSize(t *testing.T) {
	if got := sqliteBackupTimeout(0); got != 60*time.Second {
		t.Errorf("empty db timeout = %s, want 60s floor", got)
	}
	if got := sqliteBackupTimeout(1 * 1024 * 1024); got != 60*time.Second {
		t.Errorf("1MB db timeout = %s, want 60s floor", got)
	}
	big := int64(4 * 1024 * 1024 * 1024)
	want := time.Duration(big/(10*1024*1024))*time.Second + 60*time.Second
	if got := sqliteBackupTimeout(big); got != want {
		t.Errorf("4GB db timeout = %s, want %s", got, want)
	}
}
