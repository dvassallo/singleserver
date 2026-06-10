package singleserver

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestParseAddArgsAllowsFlagsAfterRepo(t *testing.T) {
	opts, err := parseAddArgs([]string{
		"smallbets/userbase-homepage",
		"--no-deploy",
		"--app-port", "8080",
		"--runtime", "node",
		"--install", "npm ci",
		"--build", "npm run build",
		"--start", "npm start",
		"--yes",
	}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if opts.repo != "smallbets/userbase-homepage" {
		t.Fatalf("unexpected repo: %s", opts.repo)
	}
	if !opts.noDeploy {
		t.Fatal("expected no-deploy")
	}
	if !opts.appPortSet || opts.appPort != 8080 {
		t.Fatalf("unexpected app port: set=%v value=%d", opts.appPortSet, opts.appPort)
	}
	if opts.runtime != "node" || opts.installCommand != "npm ci" || opts.buildCommand != "npm run build" || opts.startCommand != "npm start" {
		t.Fatalf("unexpected generated Dockerfile options: %#v", opts)
	}
	if !opts.yes {
		t.Fatal("expected yes")
	}
}

func TestParseAddArgsUsageMentionsOptions(t *testing.T) {
	_, err := parseAddArgs(nil, io.Discard)
	if err == nil {
		t.Fatal("expected usage error")
	}
	if err.Error() != addUsage {
		t.Fatalf("unexpected usage: %v", err)
	}
}

func TestParseAddArgsAcceptsGitHubURL(t *testing.T) {
	tests := []struct {
		arg  string
		want string
	}{
		{"https://github.com/smallbets/userbase-homepage", "smallbets/userbase-homepage"},
		{"https://github.com/smallbets/userbase-homepage.git", "smallbets/userbase-homepage"},
		{"https://github.com/smallbets/userbase-homepage/", "smallbets/userbase-homepage"},
	}

	for _, tt := range tests {
		opts, err := parseAddArgs([]string{tt.arg}, io.Discard)
		if err != nil {
			t.Fatalf("%s: %v", tt.arg, err)
		}
		if opts.repo != tt.want {
			t.Fatalf("%s: got %s, want %s", tt.arg, opts.repo, tt.want)
		}
	}
}

func TestParseAddArgsRejectsNonGitHubURL(t *testing.T) {
	_, err := parseAddArgs([]string{"https://example.com/smallbets/userbase-homepage"}, io.Discard)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestEnsureGitHubSetupReadyExplainsMissingSetup(t *testing.T) {
	err := ensureGitHubSetupReady(NewGitHubClient(t.TempDir()))
	if err == nil {
		t.Fatal("expected missing setup error")
	}
	text := err.Error()
	if !strings.Contains(text, "GitHub is not connected yet") || !strings.Contains(text, "singleserver github connect") {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(text, "github-app.json") {
		t.Fatalf("raw file error leaked: %v", err)
	}
}

func TestEnsureGitHubSetupReadyExplainsIncompleteSetup(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "github-app.json"), []byte(`{"app_id":123,"webhook_secret":"secret"}`), 0600); err != nil {
		t.Fatal(err)
	}

	err := ensureGitHubSetupReady(NewGitHubClient(dir))
	if err == nil {
		t.Fatal("expected incomplete setup error")
	}
	text := err.Error()
	if !strings.Contains(text, "GitHub App setup is incomplete") || !strings.Contains(text, "singleserver github connect") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPromptAddOptionsUsesDockerfileDefaults(t *testing.T) {
	opts := addOptions{repo: "smallbets/app"}
	var out bytes.Buffer
	got, err := promptAddOptions(opts, strings.NewReader("\n\n\n\n"), &out, addPromptContext{
		hasDockerfile: true,
		targetBranch:  "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.healthcheckPathSet {
		t.Fatalf("default readiness path should not be persisted: %#v", got)
	}
	if got.healthcheck != "" {
		t.Fatalf("unexpected external healthcheck: %s", got.healthcheck)
	}
	if got.noDeploy {
		t.Fatal("expected deploy to stay enabled")
	}
	text := out.String()
	if !strings.Contains(text, "Dockerfile found") {
		t.Fatalf("expected Dockerfile prompt output:\n%s", text)
	}
	if !strings.Contains(text, "Equivalent command:") {
		t.Fatalf("expected equivalent command:\n%s", text)
	}
}

func TestPromptAddOptionsFlushesBeforeReading(t *testing.T) {
	opts := addOptions{repo: "smallbets/app"}
	out := &flushCountingWriter{}
	_, err := promptAddOptions(opts, strings.NewReader("\n\n\n\n"), out, addPromptContext{
		hasDockerfile: true,
		targetBranch:  "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.flushes < 4 {
		t.Fatalf("expected each interactive prompt to flush before reading, got %d flushes\n%s", out.flushes, out.String())
	}
}

type flushCountingWriter struct {
	bytes.Buffer
	flushes int
}

func (w *flushCountingWriter) Flush() error {
	w.flushes++
	return nil
}

func TestPromptAddOptionsGeneratedNodeStaticBuild(t *testing.T) {
	input := strings.Join([]string{
		"node",
		"npm ci",
		"npm run build",
		"dist",
		"",
		"",
		"",
		"n",
	}, "\n") + "\n"
	var out bytes.Buffer
	got, err := promptAddOptions(addOptions{repo: "smallbets/site"}, strings.NewReader(input), &out, addPromptContext{
		hasDockerfile: false,
		targetBranch:  "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.runtime != "node" || got.installCommand != "npm ci" || got.buildCommand != "npm run build" || got.staticDir != "dist" {
		t.Fatalf("unexpected generated static options: %#v", got)
	}
	if got.startCommand != "" || got.appPortSet {
		t.Fatalf("static build should not prompt for start/app port: %#v", got)
	}
	if !got.noDeploy {
		t.Fatal("expected deploy to be disabled")
	}
	text := out.String()
	if !strings.Contains(text, "--runtime 'node'") || !strings.Contains(text, "--static-dir 'dist'") || !strings.Contains(text, "--no-deploy") {
		t.Fatalf("unexpected equivalent command:\n%s", text)
	}
}

func TestPromptAddOptionsGeneratedBunDynamicApp(t *testing.T) {
	input := strings.Join([]string{
		"bun",
		"bun install --frozen-lockfile",
		"",
		"",
		"bun run start",
		"abc",
		"3000",
		"",
		"/ready",
		"https://app.example.com/ready",
		"y",
	}, "\n") + "\n"
	var out bytes.Buffer
	got, err := promptAddOptions(addOptions{repo: "smallbets/app"}, strings.NewReader(input), &out, addPromptContext{
		hasDockerfile: false,
		targetBranch:  "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.runtime != "bun" || got.installCommand != "bun install --frozen-lockfile" || got.startCommand != "bun run start" {
		t.Fatalf("unexpected generated dynamic options: %#v", got)
	}
	if !got.appPortSet || got.appPort != 3000 {
		t.Fatalf("unexpected app port: %#v", got)
	}
	if !got.healthcheckPathSet || got.healthcheckPath != "/ready" {
		t.Fatalf("unexpected readiness path: %#v", got)
	}
	if got.healthcheck != "https://app.example.com/ready" {
		t.Fatalf("unexpected external healthcheck: %s", got.healthcheck)
	}
	if got.noDeploy {
		t.Fatal("expected deploy to stay enabled")
	}
	if !strings.Contains(out.String(), "Enter a port from 1 to 65535.") {
		t.Fatalf("expected invalid port guidance:\n%s", out.String())
	}
}

func TestAddOptionsUseExplicitDomains(t *testing.T) {
	opts := addOptions{repo: "dvassallo/fullsend", hosts: []string{"fullsend.game", "www.fullsend.game"}}
	app, entry, err := opts.app()
	if err != nil {
		t.Fatal(err)
	}

	if len(app.Hosts) != 2 || app.Hosts[0] != "fullsend.game" || app.Hosts[1] != "www.fullsend.game" {
		t.Fatalf("unexpected hosts: %#v", app.Hosts)
	}
	if len(entry.hosts) != 2 || entry.hosts[0] != "fullsend.game" || entry.hosts[1] != "www.fullsend.game" {
		t.Fatalf("unexpected entry hosts: %#v", entry.hosts)
	}
}

func TestParseAddArgsAcceptsRepeatedDomains(t *testing.T) {
	opts, err := parseAddArgs([]string{
		"https://github.com/dvassallo/fullsend",
		"--domain", "fullsend.game",
		"--domain", "www.fullsend.game",
	}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !opts.hostsSet {
		t.Fatal("expected hostsSet")
	}
	if len(opts.hosts) != 2 || opts.hosts[0] != "fullsend.game" || opts.hosts[1] != "www.fullsend.game" {
		t.Fatalf("unexpected domains: %#v", opts.hosts)
	}
}

func TestAppendAppToConfigYAML(t *testing.T) {
	body := []byte(`apps:
  - dvassallo/fullsend
`)
	updated, err := appendAppToConfigYAML(body, addAppEntry{
		repo:            "smallbets/userbase-homepage",
		hosts:           []string{"userbase.com", "www.userbase.com"},
		healthcheck:     "https://userbase.com/up",
		healthcheckPath: "/up",
		runtime:         "node",
		installCommand:  "npm ci",
		buildCommand:    "npm run build",
		startCommand:    "npm start",
		appPort:         8080,
		appPortSet:      true,
	})
	if err != nil {
		t.Fatal(err)
	}

	var config Config
	if err := yaml.Unmarshal(updated, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Apps) != 2 {
		t.Fatalf("expected 2 apps, got %d", len(config.Apps))
	}
	app := config.Apps[1]
	if app.Repo != "smallbets/userbase-homepage" {
		t.Fatalf("unexpected repo: %s", app.Repo)
	}
	if app.Healthcheck != "https://userbase.com/up" {
		t.Fatalf("unexpected healthcheck: %s", app.Healthcheck)
	}
	if app.AppPort != 8080 {
		t.Fatalf("unexpected app port: %d", app.AppPort)
	}
	if app.HealthcheckPath != "/up" {
		t.Fatalf("unexpected healthcheck path: %s", app.HealthcheckPath)
	}
	if app.Runtime != "node" || app.InstallCommand != "npm ci" || app.BuildCommand != "npm run build" || app.StartCommand != "npm start" {
		t.Fatalf("unexpected generated Dockerfile config: %#v", app)
	}
}

func TestAppendAppToConfigYAMLWritesStaticRuntime(t *testing.T) {
	updated, err := appendAppToConfigYAML(nil, addAppEntry{
		repo:      "smallbets/homepage",
		runtime:   "static",
		staticDir: ".",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := string(updated)
	if !strings.Contains(got, "runtime: static") {
		t.Fatalf("runtime missing:\n%s", got)
	}
	if strings.Contains(got, "static_dir") {
		t.Fatalf("default static_dir should be omitted:\n%s", got)
	}
}

func TestParseAddArgsStaticDir(t *testing.T) {
	opts, err := parseAddArgs([]string{
		"https://github.com/smallbets/userbase-homepage",
		"--runtime", "bun",
		"--install", "bun install --frozen-lockfile",
		"--build", "bun run build",
		"--static-dir", "dist",
	}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	app, entry, err := opts.app()
	if err != nil {
		t.Fatal(err)
	}
	if app.Runtime != "bun" || app.StaticDir != "dist" {
		t.Fatalf("unexpected app: %#v", app)
	}
	if entry.runtime != "bun" || entry.staticDir != "dist" {
		t.Fatalf("unexpected config entry: %#v", entry)
	}
}

func TestAppendAppToConfigYAMLUsesScalarForRepoOnly(t *testing.T) {
	updated, err := appendAppToConfigYAML(nil, addAppEntry{repo: "dvassallo/sillyface-games"})
	if err != nil {
		t.Fatal(err)
	}
	if string(updated) != "apps:\n  - dvassallo/sillyface-games\n" {
		t.Fatalf("unexpected yaml:\n%s", updated)
	}
}

func TestAppendAppToConfigYAMLRejectsDuplicateAppName(t *testing.T) {
	body := []byte(`apps:
  - alice/homepage
`)
	_, err := appendAppToConfigYAML(body, addAppEntry{repo: "bob/homepage"})
	if err == nil {
		t.Fatal("expected duplicate app name error")
	}
	if !strings.Contains(err.Error(), "duplicate app name in config") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEscapeContentPath(t *testing.T) {
	got := escapeContentPath("config/deploy.yml")
	if got != "config/deploy.yml" {
		t.Fatalf("unexpected path: %s", got)
	}
}
