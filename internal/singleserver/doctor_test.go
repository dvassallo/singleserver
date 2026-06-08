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
)

func TestDoctorAppsReturnsAllWhenNoFilter(t *testing.T) {
	apps := []AppConfig{
		{Repo: "dvassallo/fullsend", Name: "fullsend"},
		{Repo: "smallbets/userbase-homepage", Name: "userbase-homepage"},
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
		{Repo: "dvassallo/fullsend", Name: "fullsend"},
		{Repo: "smallbets/userbase-homepage", Name: "userbase-homepage"},
	}

	byName, err := doctorApps(apps, []string{"fullsend"})
	if err != nil {
		t.Fatal(err)
	}
	if len(byName) != 1 || byName[0].Repo != "dvassallo/fullsend" {
		t.Fatalf("unexpected app selected by name: %#v", byName)
	}

	byRepo, err := doctorApps(apps, []string{"smallbets/userbase-homepage"})
	if err != nil {
		t.Fatal(err)
	}
	if len(byRepo) != 1 || byRepo[0].Name != "userbase-homepage" {
		t.Fatalf("unexpected app selected by repo: %#v", byRepo)
	}
}

func TestDoctorAppsRejectsUnknownAndExtraArgs(t *testing.T) {
	apps := []AppConfig{{Repo: "dvassallo/fullsend", Name: "fullsend"}}

	if _, err := doctorApps(apps, []string{"missing"}); err == nil {
		t.Fatal("expected unknown app to fail")
	}
	if _, err := doctorApps(apps, []string{"fullsend", "extra"}); err == nil {
		t.Fatal("expected extra args to fail")
	}
}

func TestDoctorGitHubSetupPendingWithoutApps(t *testing.T) {
	var out bytes.Buffer
	ok := doctorGitHubSetup(&out, NewGitHubClient(t.TempDir()), 0)

	if !ok {
		t.Fatal("expected missing GitHub setup to be non-fatal with no apps")
	}
	if !strings.Contains(out.String(), "github\tsetup\tpending") {
		t.Fatalf("expected pending setup output, got %q", out.String())
	}
}

func TestDoctorGitHubSetupFailsWithConfiguredApps(t *testing.T) {
	var out bytes.Buffer
	ok := doctorGitHubSetup(&out, NewGitHubClient(t.TempDir()), 1)

	if ok {
		t.Fatal("expected missing GitHub setup to fail with configured apps")
	}
	if !strings.Contains(out.String(), "github\tsetup\tfailed") {
		t.Fatalf("expected failed setup output, got %q", out.String())
	}
}

func TestDoctorGitHubSetupOK(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "github-app.json"), []byte(`{"app_id":123,"slug":"single-server-test","webhook_secret":"secret"}`), 0600); err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemBody := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(filepath.Join(dir, "github-app.private-key.pem"), pemBody, 0600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	ok := doctorGitHubSetup(&out, NewGitHubClient(dir), 0)

	if !ok {
		t.Fatal("expected complete GitHub setup to pass")
	}
	if !strings.Contains(out.String(), "github\tsetup\tok\tapp_id=123\tslug=single-server-test") {
		t.Fatalf("expected ok setup output, got %q", out.String())
	}
}

func TestCloudflaredRoutes(t *testing.T) {
	config := &cloudflaredConfig{
		Ingress: []cloudflaredIngress{
			{Hostname: "Hooks.Example.com", Service: "http://127.0.0.1:8787"},
			{Hostname: "app.example.com", Service: "http://127.0.0.1:80"},
			{Service: "http_status:404"},
		},
	}

	routes := cloudflaredRoutes(config)
	if routes["hooks.example.com"] != "http://127.0.0.1:8787" {
		t.Fatalf("unexpected hook route: %#v", routes)
	}
	if routes["app.example.com"] != "http://127.0.0.1:80" {
		t.Fatalf("unexpected app route: %#v", routes)
	}
	if _, ok := routes[""]; ok {
		t.Fatal("fallback route should not be keyed")
	}
}

func TestAppsHaveHosts(t *testing.T) {
	if appsHaveHosts([]AppConfig{{Repo: "dvassallo/fullsend", Name: "fullsend"}}) {
		t.Fatal("expected no hosts")
	}
	if !appsHaveHosts([]AppConfig{{Repo: "dvassallo/fullsend", Name: "fullsend", Hosts: []string{"fullsend.game"}}}) {
		t.Fatal("expected hosts")
	}
}

func TestExpectedCloudflaredHosts(t *testing.T) {
	hosts := expectedCloudflaredHosts("Hooks.Example.com", []AppConfig{
		{Repo: "dvassallo/fullsend", Name: "fullsend", Hosts: []string{"Fullsend.Game", "fullsend.assetstacks.com"}},
		{Repo: "smallbets/userbase-homepage", Name: "userbase-homepage"},
	})

	for _, host := range []string{"hooks.example.com", "fullsend.game", "fullsend.assetstacks.com"} {
		if !hosts[host] {
			t.Fatalf("expected host %s in %#v", host, hosts)
		}
	}
	if hosts["userbase.com"] {
		t.Fatalf("unexpected host set: %#v", hosts)
	}
}

func TestStaleCloudflaredHosts(t *testing.T) {
	routes := map[string]string{
		"hooks.example.com": "http://127.0.0.1:8787",
		"old.example.com":   "http://127.0.0.1:80",
		"z.example.com":     "http://127.0.0.1:80",
	}
	expected := map[string]bool{"hooks.example.com": true}

	got := staleCloudflaredHosts(routes, expected)
	if len(got) != 2 || got[0] != "old.example.com" || got[1] != "z.example.com" {
		t.Fatalf("unexpected stale hosts: %#v", got)
	}
}

func TestFormatBytesGB(t *testing.T) {
	if got := formatBytesGB(1536 * 1024 * 1024); got != "1.5GB" {
		t.Fatalf("unexpected formatted size: %s", got)
	}
}

func TestLastDeployStatusFromJournalUsesMostRecentOutcome(t *testing.T) {
	journal := `
[deploy:fullsend-1] success total_ms=1200
[deploy:userbase-homepage-1] success total_ms=900
[deploy:fullsend-2] failed after 300ms: boom
`
	status, detail := lastDeployStatusFromJournal("fullsend", journal)
	if status != "failed" {
		t.Fatalf("unexpected status: %s", status)
	}
	if detail != "failed after 300ms: boom" {
		t.Fatalf("unexpected detail: %s", detail)
	}
}

func TestLastDeployStatusFromJournalReportsUnknown(t *testing.T) {
	status, detail := lastDeployStatusFromJournal("sillyface-games", "[server] ok")
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
