package singleserver

import (
	"io"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestParseAddArgsAllowsFlagsAfterRepo(t *testing.T) {
	opts, err := parseAddArgs([]string{
		"smallbets/userbase-homepage",
		"--host", "userbase.com",
		"--host=www.userbase.com",
		"--deploy",
		"--app-port", "8080",
	}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if opts.repo != "smallbets/userbase-homepage" {
		t.Fatalf("unexpected repo: %s", opts.repo)
	}
	if len(opts.hosts) != 2 || opts.hosts[0] != "userbase.com" || opts.hosts[1] != "www.userbase.com" {
		t.Fatalf("unexpected hosts: %#v", opts.hosts)
	}
	if !opts.deploy {
		t.Fatal("expected deploy")
	}
	if !opts.appPortSet || opts.appPort != 8080 {
		t.Fatalf("unexpected app port: set=%v value=%d", opts.appPortSet, opts.appPort)
	}
}

func TestAddOptionsAppInfersHealthcheckFromFirstHost(t *testing.T) {
	opts := addOptions{
		repo:            "smallbets/userbase-homepage",
		hosts:           repeatedStrings{"userbase.com", "www.userbase.com"},
		healthcheckPath: "ready",
	}
	app, entry, err := opts.app()
	if err != nil {
		t.Fatal(err)
	}
	if app.Healthcheck != "https://userbase.com/ready" {
		t.Fatalf("unexpected healthcheck: %s", app.Healthcheck)
	}
	if entry.healthcheck != "https://userbase.com/ready" {
		t.Fatalf("unexpected entry healthcheck: %s", entry.healthcheck)
	}
	if entry.healthcheckPath != "" {
		t.Fatalf("did not expect healthcheck_path to be written: %s", entry.healthcheckPath)
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

func TestEscapeContentPath(t *testing.T) {
	got := escapeContentPath("config/deploy.yml")
	if got != "config/deploy.yml" {
		t.Fatalf("unexpected path: %s", got)
	}
}
