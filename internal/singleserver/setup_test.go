package singleserver

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitHubConnectStoresCustomAppName(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SINGLESERVER_STATE_DIR", dir)
	t.Setenv("SINGLESERVER_CONFIG", filepath.Join(dir, "apps.yml"))

	var out bytes.Buffer
	if err := cliGitHubConnect([]string{"--name", "Single Server Test"}, &out); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "singleserver.env"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "SINGLESERVER_GITHUB_APP_NAME='Single Server Test'") {
		t.Fatalf("custom app name not stored:\n%s", body)
	}
	if !strings.Contains(out.String(), "/setup/github-app?token=") {
		t.Fatalf("setup URL not printed: %s", out.String())
	}
}
