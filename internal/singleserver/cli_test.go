package singleserver

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"text/tabwriter"
)

func TestDisplayAppDefaults(t *testing.T) {
	app := AppConfig{Repo: "acme/scoreboard", Name: "scoreboard"}

	if got := displayBranch(app); got != "(repo default)" {
		t.Fatalf("unexpected branch display: %q", got)
	}
	if got := displayHosts(app); got != "-" {
		t.Fatalf("unexpected hosts display: %q", got)
	}
	if got := displayHealthcheck(app); got != "assumed" {
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
	if !strings.Contains(got, "singleserver 1.2.3") {
		t.Fatalf("expected stamped version, got:\n%s", got)
	}
	if !strings.Contains(got, "commit 1234567890ab") {
		t.Fatalf("expected short commit, got:\n%s", got)
	}
	if !strings.Contains(got, "built  2026-06-08T21:00:00Z") {
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
	if strings.Contains(got, "singleserver init") || strings.Contains(got, "init           ") {
		t.Fatalf("did not expect init command in usage, got:\n%s", got)
	}
}

func TestParseRootCLIMode(t *testing.T) {
	tests := []struct {
		name               string
		args               []string
		envNonInteractive  string
		wantNonInteractive bool
		wantArgs           []string
		wantErr            string
	}{
		{
			name:               "root flag before command",
			args:               []string{"--non-interactive", "status"},
			wantNonInteractive: true,
			wantArgs:           []string{"status"},
		},
		{
			name:               "root parser stops at command",
			args:               []string{"status", "--non-interactive"},
			wantArgs:           []string{"status", "--non-interactive"},
			wantErr:            "",
			wantNonInteractive: false,
		},
		{
			name:               "environment enables non interactive",
			args:               []string{"status"},
			envNonInteractive:  "true",
			wantNonInteractive: true,
			wantArgs:           []string{"status"},
		},
		{
			name:    "yes is removed",
			args:    []string{"--yes", "status"},
			wantErr: "--yes has been removed",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.envNonInteractive != "" {
				t.Setenv("SINGLESERVER_NON_INTERACTIVE", test.envNonInteractive)
			}
			mode, args, err := parseRootCLIMode(test.args)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("expected %q error, got %v", test.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode.NonInteractive != test.wantNonInteractive {
				t.Fatalf("NonInteractive = %v, want %v", mode.NonInteractive, test.wantNonInteractive)
			}
			if !reflect.DeepEqual(args, test.wantArgs) {
				t.Fatalf("args = %#v, want %#v", args, test.wantArgs)
			}
		})
	}
}

func TestCommandModeFromArgsStripsNonInteractiveWithoutEatingFlagValues(t *testing.T) {
	mode, args, err := commandModeFromArgs(
		[]string{"app-name", "--healthcheck-path", "/ready", "--non-interactive", "--host=app.example.com"},
		func(arg string) bool {
			name := strings.TrimLeft(arg, "-")
			name, _, _ = strings.Cut(name, "=")
			return name == "healthcheck-path" || name == "host"
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !mode.NonInteractive {
		t.Fatal("expected non-interactive mode")
	}
	want := []string{"app-name", "--healthcheck-path", "/ready", "--host=app.example.com"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}

func TestCommandModeFromArgsRejectsRemovedYesFlag(t *testing.T) {
	_, _, err := commandModeFromArgs([]string{"--yes", "app-name"}, noFlagValues)
	if err == nil || !strings.Contains(err.Error(), "--yes has been removed") {
		t.Fatalf("expected removed --yes error, got %v", err)
	}
}

func TestNormalizeFlagArgsMovesFlagsBeforePositionals(t *testing.T) {
	got := normalizeFlagArgs(
		[]string{"app-name", "--host", "app.example.com", "--runtime=static", "--", "--literal"},
		func(arg string) bool {
			name := strings.TrimLeft(arg, "-")
			name, _, _ = strings.Cut(name, "=")
			return name == "host" || name == "runtime"
		},
	)
	want := []string{"--host", "app.example.com", "--runtime=static", "app-name", "--literal"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeFlagArgs() = %#v, want %#v", got, want)
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
	if !strings.Contains(got, "No apps configured") {
		t.Fatalf("expected empty-state message, got:\n%s", got)
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
	if !strings.Contains(got, "daemon") {
		t.Fatalf("expected daemon line, got:\n%s", got)
	}
	if !strings.Contains(got, "0 apps") {
		t.Fatalf("expected app count, got:\n%s", got)
	}
	if !strings.Contains(got, "No apps configured") {
		t.Fatalf("expected empty-state message, got:\n%s", got)
	}
	if !strings.Contains(got, "singleserver add https://github.com/owner/repo") {
		t.Fatalf("expected add hint, got:\n%s", got)
	}
}

func TestTabWriterAlignsCheckRows(t *testing.T) {
	var out bytes.Buffer
	w := tabwriter.NewWriter(&out, 0, 0, 2, ' ', 0)
	writeCheck(w, "arcade-games", "deploy", "ok", "12668ms", "ref=main", "sha=26e1a21d7082")
	writeCheck(w, "daemon", "status", "ok", "200 OK")
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}

	got := out.String()
	if strings.Contains(got, "\t") {
		t.Fatalf("expected tabwriter to expand tabs, got %q", got)
	}
	if !strings.Contains(got, "arcade-games  deploy") {
		t.Fatalf("expected first row aligned, got:\n%s", got)
	}
	if !strings.Contains(got, "daemon        status") {
		t.Fatalf("expected second row aligned, got:\n%s", got)
	}
}

func TestDisplayAppOverrides(t *testing.T) {
	app := AppConfig{
		Repo:        "acme/scoreboard",
		Name:        "scoreboard",
		Branch:      "master",
		Hosts:       []string{"scoreboard.example.com", "scoreboard-alt.example.com"},
		Healthcheck: "https://scoreboard.example.com/up",
	}

	if got := displayBranch(app); got != "master" {
		t.Fatalf("unexpected branch display: %q", got)
	}
	if got := displayHosts(app); got != "scoreboard.example.com,scoreboard-alt.example.com" {
		t.Fatalf("unexpected hosts display: %q", got)
	}
	if got := displayHealthcheck(app); got != "https://scoreboard.example.com/up" {
		t.Fatalf("unexpected healthcheck display: %q", got)
	}
}

func TestAppRuntimeStatus(t *testing.T) {
	app := AppConfig{Repo: "acme/scoreboard", Name: "scoreboard"}

	if got := appRuntimeStatus(app, map[string]string{"scoreboard-web-123": "scoreboard-web-123"}, nil); got != "running:scoreboard-web-123" {
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
		"scoreboard-web-123": "scoreboard-web-123",
		"arcade-games-1":     "arcade-games-1",
	}

	if got, ok := containerForApp("scoreboard", containers); !ok || got != "scoreboard-web-123" {
		t.Fatalf("expected scoreboard container, got %q ok=%v", got, ok)
	}
	if _, ok := containerForApp("missing", containers); ok {
		t.Fatal("expected missing app to have no container")
	}
}

func TestAppSummaryStatusWithoutHealthcheck(t *testing.T) {
	app := AppConfig{Repo: "acme/scoreboard", Name: "scoreboard"}

	if got := appSummaryStatus(app, map[string]string{"scoreboard-web-123": "scoreboard-web-123"}, nil, ""); got != "running" {
		t.Fatalf("unexpected running summary: %q", got)
	}
	if got := appSummaryStatus(app, map[string]string{}, nil, ""); got != "stopped" {
		t.Fatalf("unexpected stopped summary: %q", got)
	}
	journal := "[deploy:scoreboard-123] failed after 42ms: boom"
	if got := appSummaryStatus(app, map[string]string{"scoreboard-web-123": "scoreboard-web-123"}, nil, journal); got != "failed" {
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

	app := AppConfig{Repo: "acme/scoreboard", Name: "scoreboard", Hosts: []string{"scoreboard.example.net"}}
	if got := appLiveURL(app); got != "https://scoreboard.example.net" {
		t.Fatalf("unexpected live URL: %q", got)
	}
	if got := appLiveURL(AppConfig{Repo: "acme/scoreboard", Name: "scoreboard"}); got != "" {
		t.Fatalf("expected no live URL, got %q", got)
	}
}

func TestInspectIncludesServerSideEnvSecrets(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps.yml")
	t.Setenv("SINGLESERVER_CONFIG", configPath)
	t.Setenv("SINGLESERVER_STATE_DIR", dir)
	if err := os.WriteFile(configPath, []byte("apps:\n  - acme/scoreboard\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeAppEnv("scoreboard", map[string]string{"DATABASE_URL": "sqlite:///storage/app.db"}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := cliInspect([]string{"scoreboard"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "secret:\n    - DATABASE_URL") {
		t.Fatalf("expected rendered deploy config to include secret key, got:\n%s", out.String())
	}
}

func TestFilterJournalLogLinesDefaultsToDeployLogs(t *testing.T) {
	journal := strings.Join([]string{
		"2026-06-08T10:00:00 [server] Single Server listening",
		"2026-06-08T10:00:01 [deploy:scoreboard-123] start acme/scoreboard@abc",
		"2026-06-08T10:00:02 [deploy:marketing-site-456] success total_ms=42",
		"2026-06-08T10:00:03 [webhook:delivery] ping",
	}, "\n")

	allDeploys := filterJournalLogLines(journal, "", false)
	if len(allDeploys) != 2 {
		t.Fatalf("expected two deploy lines, got %#v", allDeploys)
	}
	scoreboardDeploys := filterJournalLogLines(journal, "scoreboard", false)
	if len(scoreboardDeploys) != 1 || !strings.Contains(scoreboardDeploys[0], "[deploy:scoreboard-123]") {
		t.Fatalf("expected scoreboard deploy line, got %#v", scoreboardDeploys)
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
