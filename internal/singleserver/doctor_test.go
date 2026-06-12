package singleserver

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDoctorAppsReturnsAllWhenNoFilter(t *testing.T) {
	apps := []AppConfig{
		{Repo: "acme/scoreboard", Name: "scoreboard"},
		{Repo: "acme/marketing-site", Name: "marketing-site"},
	}

	got, err := doctorApps(apps, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected all apps, got %d", len(got))
	}
}

func TestDoctorAppsFiltersByNameOrRepo(t *testing.T) {
	apps := []AppConfig{
		{Repo: "acme/scoreboard", Name: "scoreboard"},
		{Repo: "acme/marketing-site", Name: "marketing-site"},
	}

	byName, err := doctorApps(apps, []string{"scoreboard"})
	if err != nil {
		t.Fatal(err)
	}
	if len(byName) != 1 || byName[0].Repo != "acme/scoreboard" {
		t.Fatalf("unexpected app selected by name: %#v", byName)
	}

	byRepo, err := doctorApps(apps, []string{"acme/marketing-site"})
	if err != nil {
		t.Fatal(err)
	}
	if len(byRepo) != 1 || byRepo[0].Name != "marketing-site" {
		t.Fatalf("unexpected app selected by repo: %#v", byRepo)
	}
}

func TestDoctorAppsRejectsUnknownAndExtraArgs(t *testing.T) {
	apps := []AppConfig{{Repo: "acme/scoreboard", Name: "scoreboard"}}

	if _, err := doctorApps(apps, []string{"missing"}); err == nil {
		t.Fatal("expected unknown app to fail")
	}
	if _, err := doctorApps(apps, []string{"scoreboard", "extra"}); err == nil {
		t.Fatal("expected extra args to fail")
	}
}

func TestDoctorGitHubSetupPendingWithoutApps(t *testing.T) {
	var out bytes.Buffer
	ok := doctorGitHubSetup(&out, NewGitHubClient(t.TempDir()), 0, "")

	if !ok {
		t.Fatal("expected missing GitHub setup to be non-fatal with no apps")
	}
	if !strings.Contains(out.String(), "github\tsetup\tpending") {
		t.Fatalf("expected pending setup output, got %q", out.String())
	}
}

func TestDoctorGitHubSetupFailsWithConfiguredApps(t *testing.T) {
	var out bytes.Buffer
	ok := doctorGitHubSetup(&out, NewGitHubClient(t.TempDir()), 1, "")

	if ok {
		t.Fatal("expected missing GitHub setup to fail with configured apps")
	}
	if !strings.Contains(out.String(), "github\tsetup\tfailed") {
		t.Fatalf("expected failed setup output, got %q", out.String())
	}
}

func TestDoctorGitHubSetupOK(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "github.json"), []byte(`{"app_id":123,"slug":"single-server-test","webhook_secret":"secret"}`), 0600); err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemBody := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(filepath.Join(dir, "github.private-key.pem"), pemBody, 0600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	ok := doctorGitHubSetup(&out, NewGitHubClient(dir), 0, "")

	if !ok {
		t.Fatal("expected complete GitHub setup to pass")
	}
	if !strings.Contains(out.String(), "github\tsetup\tok\tapp_id=123\tslug=single-server-test") {
		t.Fatalf("expected ok setup output, got %q", out.String())
	}
}

func TestDoctorGitHubSetupFailsWhenWebhookTargetsDifferentServer(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "github.json"), []byte(`{"app_id":123,"slug":"single-server-test","webhook_secret":"secret"}`), 0600); err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemBody := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(filepath.Join(dir, "github.private-key.pem"), pemBody, 0600); err != nil {
		t.Fatal(err)
	}

	original := githubHookConfigFunc
	t.Cleanup(func() { githubHookConfigFunc = original })
	githubHookConfigFunc = func(*GitHubClient) (*GitHubHookConfig, error) {
		return &GitHubHookConfig{URL: "https://old.example.com/github/webhook"}, nil
	}

	var out bytes.Buffer
	ok := doctorGitHubSetup(&out, NewGitHubClient(dir), 1, "https://new.example.com/github/webhook")

	if ok {
		t.Fatal("expected webhook mismatch to fail GitHub setup")
	}
	if !strings.Contains(out.String(), "github\twebhook\tfailed\thttps://new.example.com/github/webhook\tactual=https://old.example.com/github/webhook") {
		t.Fatalf("expected webhook mismatch output, got %q", out.String())
	}
}

func TestDoctorDockerChecksBuildx(t *testing.T) {
	original := commandOutputFunc
	t.Cleanup(func() { commandOutputFunc = original })
	commandOutputFunc = func(timeout time.Duration, name string, args ...string) (string, error) {
		if name != "docker" {
			t.Fatalf("unexpected command: %s", name)
		}
		joined := strings.Join(args, " ")
		switch joined {
		case "info --format {{.ServerVersion}}":
			return "29.1.3", nil
		case "ps --format {{.Names}}":
			return "", nil
		case "buildx version":
			return "github.com/docker/buildx 0.30.1", nil
		default:
			t.Fatalf("unexpected docker args: %s", joined)
		}
		return "", nil
	}

	var out bytes.Buffer
	if !doctorDocker(&out) {
		t.Fatalf("expected Docker doctor to pass, got:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "docker\tbuildx\tok\tgithub.com/docker/buildx 0.30.1") {
		t.Fatalf("expected buildx ok output, got:\n%s", out.String())
	}
}

func TestDoctorDockerFailsWithoutBuildx(t *testing.T) {
	original := commandOutputFunc
	t.Cleanup(func() { commandOutputFunc = original })
	commandOutputFunc = func(timeout time.Duration, name string, args ...string) (string, error) {
		if name != "docker" {
			t.Fatalf("unexpected command: %s", name)
		}
		joined := strings.Join(args, " ")
		switch joined {
		case "info --format {{.ServerVersion}}":
			return "29.1.3", nil
		case "ps --format {{.Names}}":
			return "", nil
		case "buildx version":
			return "", fmt.Errorf("unknown command: docker buildx")
		default:
			t.Fatalf("unexpected docker args: %s", joined)
		}
		return "", nil
	}

	var out bytes.Buffer
	if doctorDocker(&out) {
		t.Fatalf("expected Docker doctor to fail without buildx, got:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "docker\tbuildx\tfailed\tinstall docker-buildx") {
		t.Fatalf("expected buildx failed output, got:\n%s", out.String())
	}
}

func TestDoctorDeployConfigFailsOnInvalidServerSideEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SINGLESERVER_STATE_DIR", dir)
	envDir := filepath.Join(dir, "env")
	if err := os.MkdirAll(envDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(envDir, "scoreboard.env"), []byte("not valid\n"), 0600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	ok := doctorDeployConfig(&out, AppConfig{Repo: "acme/scoreboard", Name: "scoreboard"})

	if ok {
		t.Fatal("expected invalid env file to fail deploy config check")
	}
	if !strings.Contains(out.String(), "scoreboard\tdeploy_config\tfailed") {
		t.Fatalf("expected deploy_config failure output, got:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "invalid env line") {
		t.Fatalf("expected invalid env detail, got:\n%s", out.String())
	}
}

func TestCloudflaredRoutes(t *testing.T) {
	config := &cloudflaredConfig{
		Ingress: []cloudflaredIngress{
			{Hostname: "Admin.Example.com", Service: "http://127.0.0.1:8787"},
			{Hostname: "app.example.com", Service: "http://127.0.0.1:80"},
			{Service: "http_status:404"},
		},
	}

	routes := cloudflaredRoutes(config)
	if routes["admin.example.com"] != "http://127.0.0.1:8787" {
		t.Fatalf("unexpected admin route: %#v", routes)
	}
	if routes["app.example.com"] != "http://127.0.0.1:80" {
		t.Fatalf("unexpected app route: %#v", routes)
	}
	if _, ok := routes[""]; ok {
		t.Fatal("fallback route should not be keyed")
	}
}

func TestAppsHaveHosts(t *testing.T) {
	if appsHaveHosts([]AppConfig{{Repo: "acme/scoreboard", Name: "scoreboard"}}) {
		t.Fatal("expected no hosts")
	}
	if !appsHaveHosts([]AppConfig{{Repo: "acme/scoreboard", Name: "scoreboard", Hosts: []string{"scoreboard.example.com"}}}) {
		t.Fatal("expected hosts")
	}
}

func TestExpectedCloudflaredHosts(t *testing.T) {
	hosts := expectedCloudflaredHosts([]AppConfig{
		{Repo: "acme/scoreboard", Name: "scoreboard", Hosts: []string{"Scoreboard.Example.Com", "scoreboard-alt.example.com"}},
		{Repo: "acme/marketing-site", Name: "marketing-site"},
	})

	for _, host := range []string{"scoreboard.example.com", "scoreboard-alt.example.com"} {
		if !hosts[host] {
			t.Fatalf("expected host %s in %#v", host, hosts)
		}
	}
	if hosts["admin.example.com"] {
		t.Fatalf("unexpected unrelated host set: %#v", hosts)
	}
	if hosts["marketing.example.com"] {
		t.Fatalf("unexpected host set: %#v", hosts)
	}
}

func TestStaleCloudflaredHosts(t *testing.T) {
	routes := map[string]string{
		"app.example.com": "http://127.0.0.1:80",
		"old.example.com": "http://127.0.0.1:80",
		"z.example.com":   "http://127.0.0.1:80",
	}
	expected := map[string]bool{"app.example.com": true}

	got := staleCloudflaredHosts(routes, expected)
	if len(got) != 2 || got[0] != "old.example.com" || got[1] != "z.example.com" {
		t.Fatalf("unexpected stale hosts: %#v", got)
	}
}

func TestDoctorCloudflareChecksCNAMERecords(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "cloudflared.yml")
	credentialsPath := filepath.Join(dir, "cloudflared.json")
	t.Setenv("SINGLESERVER_STATE_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "cloudflare.json"), []byte(`{
  "api_token": "token",
  "account_id": "account",
  "tunnel_id": "tunnel",
  "credentials_file": "`+credentialsPath+`",
  "config_file": "`+configPath+`"
}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credentialsPath, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`ingress:
  - hostname: app.example.com
    service: http://127.0.0.1:80
  - service: http_status:404
`), 0600); err != nil {
		t.Fatal(err)
	}
	originalRun := commandRunFunc
	t.Cleanup(func() { commandRunFunc = originalRun })
	commandRunFunc = func(timeout time.Duration, name string, args ...string) error {
		if name == "getent" {
			return fmt.Errorf("local resolver should not be used in tunnel mode")
		}
		return nil
	}
	originalVerify := verifyCloudflareDNSRecordFunc
	t.Cleanup(func() { verifyCloudflareDNSRecordFunc = originalVerify })
	verifyCloudflareDNSRecordFunc = func(host string, state *CloudflareState, client *CloudflareClient) (string, error) {
		return state.TunnelID + ".cfargotunnel.com", nil
	}

	var out bytes.Buffer
	ok := doctorCloudflare(&out, []AppConfig{{Repo: "owner/app", Name: "app", Hosts: []string{"app.example.com"}}}, []AppConfig{{Repo: "owner/app", Name: "app", Hosts: []string{"app.example.com"}}})

	if !ok {
		t.Fatalf("expected Cloudflare doctor to pass, got:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "app\tcloudflare_dns\tok\tapp.example.com\ttarget=tunnel.cfargotunnel.com") {
		t.Fatalf("expected app Cloudflare DNS ok output, got:\n%s", out.String())
	}
}

func TestDoctorCloudflareFailsOnMismatchedCNAMERecord(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "cloudflared.yml")
	credentialsPath := filepath.Join(dir, "cloudflared.json")
	t.Setenv("SINGLESERVER_STATE_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "cloudflare.json"), []byte(`{
  "api_token": "token",
  "account_id": "account",
  "tunnel_id": "tunnel",
  "credentials_file": "`+credentialsPath+`",
  "config_file": "`+configPath+`"
}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credentialsPath, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`ingress:
  - hostname: app.example.com
    service: http://127.0.0.1:80
  - service: http_status:404
`), 0600); err != nil {
		t.Fatal(err)
	}
	stubCommandRun(t)
	originalVerify := verifyCloudflareDNSRecordFunc
	t.Cleanup(func() { verifyCloudflareDNSRecordFunc = originalVerify })
	verifyCloudflareDNSRecordFunc = func(host string, state *CloudflareState, client *CloudflareClient) (string, error) {
		if host == "app.example.com" {
			return state.TunnelID + ".cfargotunnel.com", fmt.Errorf("CNAME points to old.example.net")
		}
		return state.TunnelID + ".cfargotunnel.com", nil
	}

	var out bytes.Buffer
	ok := doctorCloudflare(&out, []AppConfig{{Repo: "owner/app", Name: "app", Hosts: []string{"app.example.com"}}}, []AppConfig{{Repo: "owner/app", Name: "app", Hosts: []string{"app.example.com"}}})

	if ok {
		t.Fatalf("expected Cloudflare doctor to fail, got:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "app\tcloudflare_dns\tfailed\tapp.example.com\tCNAME points to old.example.net") {
		t.Fatalf("expected app Cloudflare DNS failed output, got:\n%s", out.String())
	}
}

func TestFormatBytesGB(t *testing.T) {
	if got := formatBytesGB(1536 * 1024 * 1024); got != "1.5GB" {
		t.Fatalf("unexpected formatted size: %s", got)
	}
}

func TestLastDeployStatusFromJournalUsesMostRecentOutcome(t *testing.T) {
	journal := `
[deploy:scoreboard-1] success total_ms=1200
[deploy:marketing-site-1] success total_ms=900
[deploy:scoreboard-2] failed after 300ms: boom
`
	status, detail := lastDeployStatusFromJournal("scoreboard", journal)
	if status != "failed" {
		t.Fatalf("unexpected status: %s", status)
	}
	if detail != "failed after 300ms: boom" {
		t.Fatalf("unexpected detail: %s", detail)
	}
}

func TestLastDeployStatusFromJournalReportsUnknown(t *testing.T) {
	status, detail := lastDeployStatusFromJournal("arcade-games", "[server] ok")
	if status != "unknown" {
		t.Fatalf("unexpected status: %s", status)
	}
	if detail != "no recent deploy outcome" {
		t.Fatalf("unexpected detail: %s", detail)
	}
}

func TestCompactWhitespace(t *testing.T) {
	got := compactWhitespace(" M file\n?? other\n")
	if got != "M file ?? other" {
		t.Fatalf("unexpected value: %q", got)
	}
}

func TestHasWord(t *testing.T) {
	if !hasWord("deploy docker sudo", "docker") {
		t.Fatal("expected docker group to be found")
	}
	if hasWord("deploy dockerish sudo", "docker") {
		t.Fatal("expected partial group name not to match")
	}
}

func TestRegistryHealthStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		fmt.Fprintln(w, "{}")
	}))
	defer server.Close()
	t.Setenv("SINGLESERVER_REGISTRY_HEALTH_URL", server.URL+"/v2/")

	status, err := registryHealthStatus()
	if err != nil {
		t.Fatal(err)
	}
	if status != "200 OK" {
		t.Fatalf("unexpected status: %s", status)
	}
}
