package singleserver

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	if !strings.Contains(out.String(), "fullsend\tnext\tpending\tdeploy with `singleserver deploy dvassallo/fullsend`") {
		t.Fatalf("expected next deploy message, got:\n%s", out.String())
	}
	values, err := loadAppEnv("fullsend")
	if err != nil {
		t.Fatal(err)
	}
	if values["DATABASE_URL"] != "sqlite:///storage/app.db" {
		t.Fatalf("unexpected DATABASE_URL: %q", values["DATABASE_URL"])
	}
}
