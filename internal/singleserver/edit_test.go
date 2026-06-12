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

func TestCliEditUpdatesHealthcheckSettings(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps.yml")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	if err := os.WriteFile(configPath, []byte(`apps:
  - repo: acme/scoreboard
    hosts:
      - scoreboard.example.com
`), 0600); err != nil {
		t.Fatal(err)
	}
	stubEditPrompt(t, false)

	var out bytes.Buffer
	err := cliEdit([]string{
		"https://github.com/acme/scoreboard",
		"--healthcheck-path", "/ready",
		"--healthcheck", "https://scoreboard.example.com/ready",
		"--no-deploy",
	}, &out, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}

	config, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	app := config.Apps[0]
	if app.HealthcheckPath != "/ready" {
		t.Fatalf("unexpected healthcheck path: %s", app.HealthcheckPath)
	}
	if app.Healthcheck != "https://scoreboard.example.com/ready" {
		t.Fatalf("unexpected healthcheck: %s", app.Healthcheck)
	}
	if !strings.Contains(out.String(), "scoreboard\tconfig\tok") {
		t.Fatalf("expected config output:\n%s", out.String())
	}
}

func TestCliEditSwitchesToRepoDockerfile(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps.yml")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	if err := os.WriteFile(configPath, []byte(`apps:
  - repo: acme/app
    runtime: node
    install: npm ci
    build: npm run build
    start: npm start
    app_port: 3000
    healthcheck_path: /up
    healthcheck: https://app.example.com/up
`), 0600); err != nil {
		t.Fatal(err)
	}
	stubEditPrompt(t, false)

	var out bytes.Buffer
	err := cliEdit([]string{
		"app",
		"--dockerfile",
		"--no-healthcheck",
		"--no-deploy",
	}, &out, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}

	config, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	app := config.Apps[0]
	if app.Runtime != "" || app.InstallCommand != "" || app.BuildCommand != "" || app.StartCommand != "" || app.StaticDir != "" {
		t.Fatalf("expected generated runtime fields cleared: %#v", app)
	}
	if app.AppPort != 3000 {
		t.Fatalf("expected app_port preserved, got %d", app.AppPort)
	}
	if app.HealthcheckPath != "/" {
		t.Fatalf("expected Dockerfile readiness default, got %s", app.HealthcheckPath)
	}
	if app.Healthcheck != "" {
		t.Fatalf("expected healthcheck cleared, got %s", app.Healthcheck)
	}
}

func TestCliEditSwitchesToGeneratedStatic(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps.yml")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	if err := os.WriteFile(configPath, []byte(`apps:
  - repo: acme/site
    app_port: 3000
    healthcheck_path: /ready
`), 0600); err != nil {
		t.Fatal(err)
	}
	stubEditPrompt(t, false)

	var out bytes.Buffer
	err := cliEdit([]string{
		"site",
		"--runtime", "static",
		"--static-dir", "public",
		"--no-deploy",
	}, &out, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}

	config, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	app := config.Apps[0]
	if app.Runtime != "static" || app.StaticDir != "public" {
		t.Fatalf("unexpected generated static config: %#v", app)
	}
	if app.AppPort != 80 || app.AppPortSet {
		t.Fatalf("expected generated static app_port default, got port=%d set=%t", app.AppPort, app.AppPortSet)
	}
	if app.HealthcheckPath != "/up" {
		t.Fatalf("expected generated static readiness default, got %s", app.HealthcheckPath)
	}
	body, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "app_port:") {
		t.Fatalf("expected default app_port omitted from config:\n%s", string(body))
	}
}

func TestPromptEditOptionsSwitchesToDockerfile(t *testing.T) {
	app := AppConfig{
		Repo:           "acme/app",
		Name:           "app",
		Runtime:        "node",
		InstallCommand: "npm ci",
		StartCommand:   "npm start",
		AppPort:        3000,
		AppPortSet:     true,
		Healthcheck:    "https://app.example.com/up",
	}
	if err := app.Normalize(); err != nil {
		t.Fatal(err)
	}
	input := strings.Join([]string{
		"",
		"",
		"/ready",
		"-",
		"n",
	}, "\n") + "\n"
	var out bytes.Buffer
	opts, err := promptEditOptions(app, editOptions{appName: "app"}, strings.NewReader(input), &out, editPromptContext{
		hasDockerfile: true,
		targetBranch:  "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.dockerfile {
		t.Fatal("expected Dockerfile mode")
	}
	if !opts.noHealthcheck {
		t.Fatal("expected healthcheck to be cleared")
	}
	if !opts.noDeploy {
		t.Fatal("expected deploy disabled")
	}
	if !strings.Contains(out.String(), "Equivalent command:") || !strings.Contains(out.String(), "--dockerfile") || !strings.Contains(out.String(), "--no-healthcheck") {
		t.Fatalf("unexpected prompt output:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "--non-interactive") {
		t.Fatalf("expected equivalent command to be scriptable:\n%s", out.String())
	}
}

func stubEditPrompt(t *testing.T, interactive bool) {
	t.Helper()
	original := editPromptInteractiveFunc
	t.Cleanup(func() { editPromptInteractiveFunc = original })
	editPromptInteractiveFunc = func() bool { return interactive }
}
