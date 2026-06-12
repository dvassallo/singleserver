package singleserver

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoveRejectsRemovedYesFlag(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps.yml")
	storagePath := filepath.Join(dir, "storage")
	repoPath := filepath.Join(dir, "repos", "fullsend")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	t.Setenv("SINGLESERVER_STATE_DIR", dir)
	if err := os.MkdirAll(storagePath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(repoPath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`apps:
  - repo: dvassallo/fullsend
    path: `+repoPath+`
    storage:
      path: `+storagePath+`
      mount: /storage
`), 0600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err := cliRemove([]string{"fullsend", "--delete-storage", "--yes"}, &out)
	if err == nil || !strings.Contains(err.Error(), "--yes has been removed") {
		t.Fatalf("expected removed --yes error, got %v", err)
	}
	if _, err := os.Stat(storagePath); err != nil {
		t.Fatalf("expected storage kept: %v", err)
	}
	if _, err := os.Stat(repoPath); err != nil {
		t.Fatalf("expected repo checkout kept: %v", err)
	}
	config, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Apps) != 1 {
		t.Fatalf("expected app config kept, got %#v", config.Apps)
	}
}

func TestRemoveKeepsConfigWhenCloudflareFails(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps.yml")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	t.Setenv("SINGLESERVER_STATE_DIR", dir)
	if err := os.WriteFile(configPath, []byte(`apps:
  - repo: dvassallo/fullsend
    hosts:
      - play.nobrainer.host
`), 0600); err != nil {
		t.Fatal(err)
	}

	originalSync := syncCloudflareAppDomainFunc
	t.Cleanup(func() { syncCloudflareAppDomainFunc = originalSync })
	syncCloudflareAppDomainFunc = func(hostname string, add bool, w io.Writer) error {
		if !add {
			return errors.New("cloudflare unavailable")
		}
		return nil
	}

	var out bytes.Buffer
	err := cliRemove([]string{"fullsend", "--non-interactive"}, &out)
	if err == nil {
		t.Fatal("expected Cloudflare error")
	}
	if !strings.Contains(err.Error(), "cloudflare unavailable") {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(out.String(), "config\tok\tremoved") {
		t.Fatalf("unexpected removal output: %s", out.String())
	}

	config, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Apps) != 1 || config.Apps[0].Repo != "dvassallo/fullsend" {
		t.Fatalf("expected app config kept, got %#v", config.Apps)
	}
}

func TestRemoveKeepsFilesWhenContainerStopFails(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps.yml")
	storagePath := filepath.Join(dir, "storage")
	repoPath := filepath.Join(dir, "repos", "fullsend")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	t.Setenv("SINGLESERVER_STATE_DIR", dir)
	if err := os.MkdirAll(storagePath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(repoPath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`apps:
  - repo: dvassallo/fullsend
    path: `+repoPath+`
    storage:
      path: `+storagePath+`
      mount: /storage
`), 0600); err != nil {
		t.Fatal(err)
	}

	originalStop := stopAppContainersFunc
	t.Cleanup(func() { stopAppContainersFunc = originalStop })
	stopAppContainersFunc = func(appName string) error {
		return errors.New("docker unavailable")
	}

	var out bytes.Buffer
	err := cliRemove([]string{"fullsend", "--delete-storage", "--delete-repo", "--non-interactive"}, &out)
	if err == nil {
		t.Fatal("expected container stop error")
	}
	if !strings.Contains(err.Error(), "docker unavailable") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(storagePath); err != nil {
		t.Fatalf("expected storage kept: %v", err)
	}
	if _, err := os.Stat(repoPath); err != nil {
		t.Fatalf("expected repo checkout kept: %v", err)
	}
}

func TestRemoveDeleteStorageWithExplicitFlags(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps.yml")
	storagePath := filepath.Join(dir, "storage")
	repoPath := filepath.Join(dir, "repos", "fullsend")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	t.Setenv("SINGLESERVER_STATE_DIR", dir)
	if err := os.MkdirAll(storagePath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(repoPath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storagePath, "data.txt"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoPath, "README.md"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`apps:
  - repo: dvassallo/fullsend
    path: `+repoPath+`
    storage:
      path: `+storagePath+`
      mount: /storage
`), 0600); err != nil {
		t.Fatal(err)
	}
	originalStop := stopAppContainersFunc
	t.Cleanup(func() { stopAppContainersFunc = originalStop })
	stopAppContainersFunc = func(appName string) error {
		return nil
	}

	var out bytes.Buffer
	if err := cliRemove([]string{"fullsend", "--delete-storage", "--delete-repo", "--non-interactive"}, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(storagePath); !os.IsNotExist(err) {
		t.Fatalf("expected storage deleted, stat err=%v", err)
	}
	if _, err := os.Stat(repoPath); !os.IsNotExist(err) {
		t.Fatalf("expected repo checkout deleted, stat err=%v", err)
	}
	config, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Apps) != 0 {
		t.Fatalf("expected app config removed, got %#v", config.Apps)
	}
}
