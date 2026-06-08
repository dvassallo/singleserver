package singleserver

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDisplayAppDefaults(t *testing.T) {
	app := AppConfig{Repo: "dvassallo/fullsend", Name: "fullsend"}

	if got := displayBranch(app); got != "(repo default)" {
		t.Fatalf("unexpected branch display: %q", got)
	}
	if got := displayHosts(app); got != "-" {
		t.Fatalf("unexpected hosts display: %q", got)
	}
	if got := displayHealthcheck(app); got != "-" {
		t.Fatalf("unexpected healthcheck display: %q", got)
	}
}

func TestPrintVersionUsesStampedBuildValues(t *testing.T) {
	originalVersion, originalCommit, originalBuildDate := Version, Commit, BuildDate
	t.Cleanup(func() {
		Version = originalVersion
		Commit = originalCommit
		BuildDate = originalBuildDate
	})
	Version = "1.2.3"
	Commit = "1234567890abcdef"
	BuildDate = "2026-06-08T21:00:00Z"

	var out bytes.Buffer
	printVersion(&out)

	got := out.String()
	if !strings.Contains(got, "singleserver\tversion\t1.2.3") {
		t.Fatalf("expected stamped version, got:\n%s", got)
	}
	if !strings.Contains(got, "commit=1234567890ab") {
		t.Fatalf("expected short commit, got:\n%s", got)
	}
	if !strings.Contains(got, "built=2026-06-08T21:00:00Z") {
		t.Fatalf("expected build date, got:\n%s", got)
	}
}

func TestUsageMentionsVersionCommand(t *testing.T) {
	var out bytes.Buffer
	printUsage(&out)

	got := out.String()
	if !strings.Contains(got, "singleserver version") {
		t.Fatalf("expected version usage, got:\n%s", got)
	}
	if !strings.Contains(got, "version        Print the installed Single Server version.") {
		t.Fatalf("expected version command description, got:\n%s", got)
	}
}

func TestListShowsFirstAppHintWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps.yml")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	if err := os.WriteFile(configPath, []byte("apps: []\n"), 0600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := cliList(&out); err != nil {
		t.Fatal(err)
	}

	got := out.String()
	if !strings.Contains(got, "apps\t0") {
		t.Fatalf("expected app count, got:\n%s", got)
	}
	if !strings.Contains(got, "singleserver add https://github.com/owner/repo") {
		t.Fatalf("expected add hint, got:\n%s", got)
	}
}

func TestStatusShowsConfigAndFirstAppHintWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps.yml")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	t.Setenv("SINGLESERVER_PORT", "0")
	if err := os.WriteFile(configPath, []byte("apps: []\n"), 0600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := cliStatus(&out); err != nil {
		t.Fatal(err)
	}

	got := out.String()
	if !strings.Contains(got, "config\tok\t"+configPath+"\tapps=0") {
		t.Fatalf("expected config summary, got:\n%s", got)
	}
	if !strings.Contains(got, "apps\t0") {
		t.Fatalf("expected app count, got:\n%s", got)
	}
	if !strings.Contains(got, "singleserver add https://github.com/owner/repo") {
		t.Fatalf("expected add hint, got:\n%s", got)
	}
}

func TestDisplayAppOverrides(t *testing.T) {
	app := AppConfig{
		Repo:        "dvassallo/fullsend",
		Name:        "fullsend",
		Branch:      "master",
		Hosts:       []string{"fullsend.game", "fullsend.assetstacks.com"},
		Healthcheck: "https://fullsend.game/up",
	}

	if got := displayBranch(app); got != "master" {
		t.Fatalf("unexpected branch display: %q", got)
	}
	if got := displayHosts(app); got != "fullsend.game,fullsend.assetstacks.com" {
		t.Fatalf("unexpected hosts display: %q", got)
	}
	if got := displayHealthcheck(app); got != "https://fullsend.game/up" {
		t.Fatalf("unexpected healthcheck display: %q", got)
	}
}

func TestAppRuntimeStatus(t *testing.T) {
	app := AppConfig{Repo: "dvassallo/fullsend", Name: "fullsend"}

	if got := appRuntimeStatus(app, map[string]string{"fullsend-web-123": "fullsend-web-123"}, nil); got != "running:fullsend-web-123" {
		t.Fatalf("unexpected running status: %q", got)
	}
	if got := appRuntimeStatus(app, map[string]string{"other": "other"}, nil); got != "stopped" {
		t.Fatalf("unexpected stopped status: %q", got)
	}
	if got := appRuntimeStatus(app, nil, errTestDockerUnavailable{}); got != "unknown:docker unavailable" {
		t.Fatalf("unexpected unknown status: %q", got)
	}
}

func TestContainerForApp(t *testing.T) {
	containers := map[string]string{
		"fullsend-web-123":  "fullsend-web-123",
		"sillyface-games-1": "sillyface-games-1",
	}

	if got, ok := containerForApp("fullsend", containers); !ok || got != "fullsend-web-123" {
		t.Fatalf("expected fullsend container, got %q ok=%v", got, ok)
	}
	if _, ok := containerForApp("missing", containers); ok {
		t.Fatal("expected missing app to have no container")
	}
}

func TestAppSummaryStatusWithoutHealthcheck(t *testing.T) {
	app := AppConfig{Repo: "dvassallo/fullsend", Name: "fullsend"}

	if got := appSummaryStatus(app, map[string]string{"fullsend-web-123": "fullsend-web-123"}, nil, ""); got != "running" {
		t.Fatalf("unexpected running summary: %q", got)
	}
	if got := appSummaryStatus(app, map[string]string{}, nil, ""); got != "stopped" {
		t.Fatalf("unexpected stopped summary: %q", got)
	}
	journal := "[deploy:fullsend-123] failed after 42ms: boom"
	if got := appSummaryStatus(app, map[string]string{"fullsend-web-123": "fullsend-web-123"}, nil, journal); got != "failed" {
		t.Fatalf("unexpected failed summary: %q", got)
	}
}

func TestDeployOutputHelpers(t *testing.T) {
	if got := shortSHA("1234567890abcdef"); got != "1234567890ab" {
		t.Fatalf("unexpected short sha: %q", got)
	}
	if got := shortSHA("abc123"); got != "abc123" {
		t.Fatalf("unexpected short sha: %q", got)
	}

	app := AppConfig{Repo: "dvassallo/fullsend", Name: "fullsend", Hosts: []string{"fullsend.nobrainer.host"}}
	if got := appLiveURL(app); got != "https://fullsend.nobrainer.host" {
		t.Fatalf("unexpected live URL: %q", got)
	}
	if got := appLiveURL(AppConfig{Repo: "dvassallo/fullsend", Name: "fullsend"}); got != "" {
		t.Fatalf("expected no live URL, got %q", got)
	}
}

func TestRenderDeployIncludesServerSideEnvSecrets(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps.yml")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	t.Setenv("SINGLESERVER_STATE_DIR", dir)
	if err := os.WriteFile(configPath, []byte("apps:\n  - dvassallo/fullsend\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeAppEnv("fullsend", map[string]string{"DATABASE_URL": "sqlite:///storage/app.db"}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := cliRenderDeploy([]string{"fullsend"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "secret:\n    - DATABASE_URL") {
		t.Fatalf("expected rendered deploy config to include secret key, got:\n%s", out.String())
	}
}

func TestFilterJournalLogLinesDefaultsToDeployLogs(t *testing.T) {
	journal := strings.Join([]string{
		"2026-06-08T10:00:00 [server] Single Server listening",
		"2026-06-08T10:00:01 [deploy:fullsend-123] start dvassallo/fullsend@abc",
		"2026-06-08T10:00:02 [deploy:userbase-homepage-456] success total_ms=42",
		"2026-06-08T10:00:03 [webhook:delivery] ping",
	}, "\n")

	allDeploys := filterJournalLogLines(journal, "", false)
	if len(allDeploys) != 2 {
		t.Fatalf("expected two deploy lines, got %#v", allDeploys)
	}
	fullsendDeploys := filterJournalLogLines(journal, "fullsend", false)
	if len(fullsendDeploys) != 1 || !strings.Contains(fullsendDeploys[0], "[deploy:fullsend-123]") {
		t.Fatalf("expected fullsend deploy line, got %#v", fullsendDeploys)
	}
	daemonLines := filterJournalLogLines(journal, "", true)
	if len(daemonLines) != 4 {
		t.Fatalf("expected full daemon journal, got %#v", daemonLines)
	}
}

type errTestDockerUnavailable struct{}

func (errTestDockerUnavailable) Error() string {
	return "docker unavailable"
}
