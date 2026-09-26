package singleserver

import (
	"bytes"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDomainsAndStorageCommandsUpdateConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps.yml")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	stubCommandRun(t)
	if err := os.WriteFile(configPath, []byte("apps:\n  - acme/scoreboard\n"), 0600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	logger := log.New(io.Discard, "", 0)
	if err := cliDomains([]string{"add", "scoreboard", "play.example.com", "--no-deploy"}, &out, logger); err != nil {
		t.Fatal(err)
	}
	storagePath := filepath.Join(dir, "storage")
	if err := cliStorage([]string{"enable", "scoreboard", "--path", storagePath, "--mount", "/data", "--no-deploy"}, &out, logger); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "scoreboard\tnext\tpending\tdeploy with `singleserver deploy scoreboard`") {
		t.Fatalf("expected staged deploy message, got:\n%s", out.String())
	}

	config, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	app := config.Apps[0]
	if len(app.Hosts) != 1 || app.Hosts[0] != "play.example.com" {
		t.Fatalf("unexpected hosts: %#v", app.Hosts)
	}
	if app.Healthcheck != "" {
		t.Fatalf("unexpected healthcheck: %s", app.Healthcheck)
	}
	if app.Storage == nil || app.Storage.Path != storagePath || app.Storage.Mount != "/data" {
		t.Fatalf("unexpected storage: %#v", app.Storage)
	}
}

func TestStorageDisableClearsConfigAndOptionallyDeletes(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps.yml")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	stubCommandRun(t)
	if err := os.WriteFile(configPath, []byte("apps:\n  - acme/scoreboard\n"), 0600); err != nil {
		t.Fatal(err)
	}
	storagePath := filepath.Join(dir, "storage")
	logger := log.New(io.Discard, "", 0)

	var out bytes.Buffer
	if err := cliStorage([]string{"enable", "scoreboard", "--path", storagePath, "--no-deploy", "--non-interactive"}, &out, logger); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(storagePath); err != nil {
		t.Fatalf("expected storage dir created: %v", err)
	}

	// Disable without --delete keeps the directory.
	out.Reset()
	if err := cliStorage([]string{"disable", "scoreboard", "--no-deploy", "--non-interactive"}, &out, logger); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if config.Apps[0].Storage != nil {
		t.Fatalf("expected storage cleared, got %#v", config.Apps[0].Storage)
	}
	if !strings.Contains(out.String(), "scoreboard\tstorage\tkept\t"+storagePath) {
		t.Fatalf("expected kept message, got:\n%s", out.String())
	}
	if _, err := os.Stat(storagePath); err != nil {
		t.Fatalf("expected storage dir kept: %v", err)
	}

	// Re-enable, then disable with --delete removes the directory.
	out.Reset()
	if err := cliStorage([]string{"enable", "scoreboard", "--path", storagePath, "--no-deploy", "--non-interactive"}, &out, logger); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := cliStorage([]string{"disable", "scoreboard", "--delete", "--no-deploy", "--non-interactive"}, &out, logger); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "scoreboard\tstorage\tok\t"+storagePath+"\tdeleted") {
		t.Fatalf("expected deleted message, got:\n%s", out.String())
	}
	if _, err := os.Stat(storagePath); !os.IsNotExist(err) {
		t.Fatalf("expected storage dir deleted, stat err=%v", err)
	}
}

func TestStorageDisableErrorsWhenNotEnabled(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps.yml")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	stubCommandRun(t)
	if err := os.WriteFile(configPath, []byte("apps:\n  - acme/scoreboard\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	logger := log.New(io.Discard, "", 0)
	err := cliStorage([]string{"disable", "scoreboard", "--no-deploy", "--non-interactive"}, &out, logger)
	if err == nil || !strings.Contains(err.Error(), "no storage configured") {
		t.Fatalf("expected no-storage error, got: %v", err)
	}
}

func TestStorageEnableFailsBeforeConfigWriteWhenOwnershipFixFails(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps.yml")
	storagePath := filepath.Join(dir, "storage")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	if err := os.WriteFile(configPath, []byte("apps:\n  - acme/scoreboard\n"), 0600); err != nil {
		t.Fatal(err)
	}

	originalRun := commandRunFunc
	t.Cleanup(func() { commandRunFunc = originalRun })
	commandRunFunc = func(timeout time.Duration, name string, args ...string) error {
		return errors.New("chown failed")
	}
	originalWriteConfig := writeConfigFunc
	t.Cleanup(func() { writeConfigFunc = originalWriteConfig })
	writeConfigCalled := false
	writeConfigFunc = func(path string, config *Config) error {
		writeConfigCalled = true
		return originalWriteConfig(path, config)
	}

	var out bytes.Buffer
	logger := log.New(io.Discard, "", 0)
	err := cliStorage([]string{"enable", "scoreboard", "--path", storagePath, "--no-deploy", "--non-interactive"}, &out, logger)
	if err == nil {
		t.Fatal("expected chown error")
	}
	if !strings.Contains(err.Error(), "chown "+storagePath+" to deploy:docker") {
		t.Fatalf("unexpected error: %v", err)
	}
	if writeConfigCalled {
		t.Fatal("did not expect config write after chown failure")
	}
	if strings.Contains(out.String(), "storage\tok") {
		t.Fatalf("unexpected success output: %s", out.String())
	}
	if _, err := os.Stat(storagePath); !os.IsNotExist(err) {
		t.Fatalf("expected newly-created storage directory to be removed, stat err=%v", err)
	}
}

func TestStorageEnableReportsSuccessOnlyAfterConfigWrite(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps.yml")
	storagePath := filepath.Join(dir, "storage")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	stubCommandRun(t)
	if err := os.WriteFile(configPath, []byte("apps:\n  - acme/scoreboard\n"), 0600); err != nil {
		t.Fatal(err)
	}

	originalWriteConfig := writeConfigFunc
	t.Cleanup(func() { writeConfigFunc = originalWriteConfig })
	writeConfigFunc = func(path string, config *Config) error {
		return errors.New("config write failed")
	}

	var out bytes.Buffer
	logger := log.New(io.Discard, "", 0)
	err := cliStorage([]string{"enable", "scoreboard", "--path", storagePath, "--no-deploy", "--non-interactive"}, &out, logger)
	if err == nil {
		t.Fatal("expected config write error")
	}
	if !strings.Contains(err.Error(), "config write failed") {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(out.String(), "storage\tok") {
		t.Fatalf("unexpected success output: %s", out.String())
	}
	if _, err := os.Stat(storagePath); !os.IsNotExist(err) {
		t.Fatalf("expected newly-created storage directory to be removed, stat err=%v", err)
	}
}
