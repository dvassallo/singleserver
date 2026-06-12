package singleserver

import (
	"runtime"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestGeneratedDeployYAMLUsesConventionsAndOverrides(t *testing.T) {
	t.Setenv("SINGLESERVER_STATE_DIR", t.TempDir())

	body, err := GeneratedDeployYAML(AppConfig{
		Repo:            "acme/marketing-site",
		Hosts:           []string{"marketing.example.com", "www.marketing.example.com"},
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

	if config["service"] != "marketing-site" {
		t.Fatalf("unexpected service: %v", config["service"])
	}
	if config["image"] != "marketing-site" {
		t.Fatalf("unexpected image: %v", config["image"])
	}
	builder := config["builder"].(map[string]any)
	if builder["arch"] != runtime.GOARCH {
		t.Fatalf("unexpected builder arch: %v", builder["arch"])
	}

	ssh := config["ssh"].(map[string]any)
	if ssh["user"] != "deploy" {
		t.Fatalf("unexpected ssh user: %v", ssh["user"])
	}
	keys := ssh["keys"].([]any)
	if len(keys) != 1 || keys[0] != "/root/.ssh/id_ed25519" {
		t.Fatalf("unexpected ssh keys: %#v", keys)
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
	run := proxy["run"].(map[string]any)
	bindIPs := run["bind_ips"].([]any)
	if len(bindIPs) != 1 || bindIPs[0] != "127.0.0.1" {
		t.Fatalf("unexpected proxy bind IPs: %#v", bindIPs)
	}

	hosts := proxy["hosts"].([]any)
	if len(hosts) != 2 || hosts[0] != "marketing.example.com" || hosts[1] != "www.marketing.example.com" {
		t.Fatalf("unexpected hosts: %#v", hosts)
	}

	healthcheck := proxy["healthcheck"].(map[string]any)
	if healthcheck["path"] != "/ready" {
		t.Fatalf("unexpected healthcheck path: %v", healthcheck["path"])
	}
}

func TestGeneratedDeployYAMLKeepsProxySSLDisabled(t *testing.T) {
	t.Setenv("SINGLESERVER_STATE_DIR", t.TempDir())

	body, err := GeneratedDeployYAML(AppConfig{
		Repo:  "acme/marketing-site",
		Hosts: []string{"marketing.example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}

	var config map[string]any
	if err := yaml.Unmarshal(body, &config); err != nil {
		t.Fatal(err)
	}
	proxy := config["proxy"].(map[string]any)
	if proxy["ssl"] != false {
		t.Fatalf("unexpected ssl: %v", proxy["ssl"])
	}
}

func TestGeneratedDeployYAMLOmitsEmptyProxyHosts(t *testing.T) {
	t.Setenv("SINGLESERVER_STATE_DIR", t.TempDir())

	body, err := GeneratedDeployYAML(AppConfig{Repo: "acme/arcade-games"})
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
	if healthcheck["path"] != "/" {
		t.Fatalf("unexpected default healthcheck path: %v", healthcheck["path"])
	}
}

func TestGeneratedDeployYAMLIncludesSecretsAndStorage(t *testing.T) {
	body, err := GeneratedDeployYAML(AppConfig{
		Repo:          "acme/scoreboard",
		SecretEnvKeys: []string{"ADMIN_PASSWORD", "STRIPE_SECRET_KEY"},
		Storage: &StorageConfig{
			Path:  "/srv/storage/scoreboard",
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
	if len(volumes) != 1 || volumes[0] != "/srv/storage/scoreboard:/storage" {
		t.Fatalf("unexpected volumes: %#v", volumes)
	}
}
