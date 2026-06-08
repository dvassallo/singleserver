package singleserver

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestGeneratedDeployYAMLUsesConventionsAndOverrides(t *testing.T) {
	body, err := GeneratedDeployYAML(AppConfig{
		Repo:            "smallbets/userbase-homepage",
		Hosts:           []string{"userbase.com", "www.userbase.com"},
		AppPort:         8080,
		HealthcheckPath: "/ready",
	})
	if err != nil {
		t.Fatal(err)
	}

	var config map[string]any
	if err := yaml.Unmarshal(body, &config); err != nil {
		t.Fatal(err)
	}

	if config["service"] != "userbase-homepage" {
		t.Fatalf("unexpected service: %v", config["service"])
	}
	if config["image"] != "userbase-homepage" {
		t.Fatalf("unexpected image: %v", config["image"])
	}

	proxy := config["proxy"].(map[string]any)
	if proxy["app_port"] != 8080 {
		t.Fatalf("unexpected app_port: %v", proxy["app_port"])
	}
	if proxy["ssl"] != false {
		t.Fatalf("unexpected ssl: %v", proxy["ssl"])
	}
	if proxy["forward_headers"] != true {
		t.Fatalf("unexpected forward_headers: %v", proxy["forward_headers"])
	}

	hosts := proxy["hosts"].([]any)
	if len(hosts) != 2 || hosts[0] != "userbase.com" || hosts[1] != "www.userbase.com" {
		t.Fatalf("unexpected hosts: %#v", hosts)
	}

	healthcheck := proxy["healthcheck"].(map[string]any)
	if healthcheck["path"] != "/ready" {
		t.Fatalf("unexpected healthcheck path: %v", healthcheck["path"])
	}
}

func TestGeneratedDeployYAMLOmitsEmptyProxyHosts(t *testing.T) {
	body, err := GeneratedDeployYAML(AppConfig{Repo: "dvassallo/sillyface-games"})
	if err != nil {
		t.Fatal(err)
	}

	var config map[string]any
	if err := yaml.Unmarshal(body, &config); err != nil {
		t.Fatal(err)
	}
	proxy := config["proxy"].(map[string]any)
	if _, ok := proxy["hosts"]; ok {
		t.Fatalf("expected empty hosts to be omitted: %#v", proxy["hosts"])
	}
	if proxy["app_port"] != 80 {
		t.Fatalf("unexpected default app_port: %v", proxy["app_port"])
	}
	healthcheck := proxy["healthcheck"].(map[string]any)
	if healthcheck["path"] != "/up" {
		t.Fatalf("unexpected default healthcheck path: %v", healthcheck["path"])
	}
}

func TestGeneratedDeployYAMLIncludesSecretsAndStorage(t *testing.T) {
	body, err := GeneratedDeployYAML(AppConfig{
		Repo:          "dvassallo/fullsend",
		SecretEnvKeys: []string{"ADMIN_PASSWORD", "STRIPE_SECRET_KEY"},
		Storage: &StorageConfig{
			Path:  "/srv/storage/fullsend",
			Mount: "/storage",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var config map[string]any
	if err := yaml.Unmarshal(body, &config); err != nil {
		t.Fatal(err)
	}
	env := config["env"].(map[string]any)
	secrets := env["secret"].([]any)
	if len(secrets) != 2 || secrets[0] != "ADMIN_PASSWORD" || secrets[1] != "STRIPE_SECRET_KEY" {
		t.Fatalf("unexpected secrets: %#v", secrets)
	}
	volumes := config["volumes"].([]any)
	if len(volumes) != 1 || volumes[0] != "/srv/storage/fullsend:/storage" {
		t.Fatalf("unexpected volumes: %#v", volumes)
	}
}
