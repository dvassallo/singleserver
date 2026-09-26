package singleserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestOutputGroupsChecksAsText(t *testing.T) {
	var buf bytes.Buffer
	o := newTextOutput(&buf)
	writeCheck(o, "docker", "server", "ok", "27.5")
	writeCheck(o, "docker", "buildx", "ok", "0.30")
	writeCheck(o, "fullsend", "checkout", "ok", "master")
	if err := o.Flush(); err != nil {
		t.Fatal(err)
	}

	got := buf.String()
	if strings.Contains(got, "\t") {
		t.Fatalf("text output should not contain tabs:\n%s", got)
	}
	for _, want := range []string{"docker\n", "  ✓ server", "  ✓ buildx", "fullsend\n", "  ✓ checkout"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	// docker group must render before the fullsend group.
	if strings.Index(got, "docker\n") > strings.Index(got, "fullsend\n") {
		t.Fatalf("groups out of order:\n%s", got)
	}
}

func TestOutputChecksJSON(t *testing.T) {
	var buf bytes.Buffer
	o := newJSONOutput(&buf)
	writeCheck(o, "github", "setup", "ok", "app_id=123", "slug=single-server")
	writeCheck(o, "fullsend", "healthcheck", "failed", "https://x", "boom")
	if err := o.Flush(); err != nil {
		t.Fatal(err)
	}

	var payload struct {
		OK     bool          `json:"ok"`
		Checks []ReportCheck `json:"checks"`
	}
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, buf.String())
	}
	if payload.OK {
		t.Fatal("expected ok=false because a check failed")
	}
	if len(payload.Checks) != 2 {
		t.Fatalf("want 2 checks, got %d", len(payload.Checks))
	}
	first := payload.Checks[0]
	if first.Scope != "github" || first.Check != "setup" || first.Status != "ok" {
		t.Fatalf("unexpected first check: %+v", first)
	}
	if first.Value != "app_id=123 slug=single-server" {
		t.Fatalf("unexpected combined value: %q", first.Value)
	}
}

func TestOutputJSONSuppressesNotes(t *testing.T) {
	var buf bytes.Buffer
	o := newJSONOutput(&buf)
	writeCheck(o, "github", "connect", "ok", "https://example.ts.net/setup")
	fmt.Fprintln(o, "Open the setup URL, then rerun your command.")
	if err := o.Flush(); err != nil {
		t.Fatal(err)
	}

	got := buf.String()
	if strings.Contains(got, "Open the setup URL") {
		t.Fatalf("human note leaked into json:\n%s", got)
	}
	if !strings.Contains(got, "https://example.ts.net/setup") {
		t.Fatalf("check value missing from json:\n%s", got)
	}
	if !json.Valid(buf.Bytes()) {
		t.Fatalf("json output is not valid:\n%s", got)
	}
}

func TestOutputTextPrintsNoteAfterChecks(t *testing.T) {
	var buf bytes.Buffer
	o := newTextOutput(&buf)
	writeCheck(o, "github", "connect", "ok", "https://example.ts.net/setup")
	fmt.Fprintln(o, "Open the setup URL")
	if err := o.Flush(); err != nil {
		t.Fatal(err)
	}

	got := buf.String()
	check := strings.Index(got, "connect")
	note := strings.Index(got, "Open the setup URL")
	if check < 0 || note < 0 || check > note {
		t.Fatalf("expected the check to render before the note:\n%s", got)
	}
}

func TestOutputVersionJSON(t *testing.T) {
	var buf bytes.Buffer
	o := newJSONOutput(&buf)
	o.versionInfo(VersionView{Version: "1.2.3", Commit: "abcdef", Built: "2026-06-13T00:00:00Z"})
	if err := o.Flush(); err != nil {
		t.Fatal(err)
	}

	var v VersionView
	if err := json.Unmarshal(buf.Bytes(), &v); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, buf.String())
	}
	if v.Version != "1.2.3" || v.Commit != "abcdef" {
		t.Fatalf("unexpected version payload: %+v", v)
	}
}

func TestOutputListJSON(t *testing.T) {
	var buf bytes.Buffer
	o := newJSONOutput(&buf)
	o.listApps([]AppView{{Name: "fullsend", Repo: "dvassallo/fullsend", State: "running", Hosts: []string{"fullsend.game", "www.fullsend.game"}}})
	if err := o.Flush(); err != nil {
		t.Fatal(err)
	}

	var payload struct {
		Apps []AppView `json:"apps"`
	}
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, buf.String())
	}
	if len(payload.Apps) != 1 {
		t.Fatalf("want 1 app, got %d", len(payload.Apps))
	}
	app := payload.Apps[0]
	if app.Name != "fullsend" || app.State != "running" || len(app.Hosts) != 2 {
		t.Fatalf("unexpected app payload: %+v", app)
	}
}

func TestOutputStatusJSON(t *testing.T) {
	var buf bytes.Buffer
	o := newJSONOutput(&buf)
	o.statusReport(DaemonView{State: "ok", Apps: 1}, []AppView{{
		Name:   "fullsend",
		State:  "running",
		Deploy: &DeployView{State: "ok", Detail: "deployed in 5.7s"},
		Health: &HealthView{State: "ok", URL: "fullsend.game/up"},
	}})
	if err := o.Flush(); err != nil {
		t.Fatal(err)
	}

	var payload struct {
		Daemon DaemonView `json:"daemon"`
		Apps   []AppView  `json:"apps"`
	}
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, buf.String())
	}
	if payload.Daemon.State != "ok" || payload.Daemon.Apps != 1 {
		t.Fatalf("unexpected daemon: %+v", payload.Daemon)
	}
	if len(payload.Apps) != 1 || payload.Apps[0].Deploy == nil || payload.Apps[0].Deploy.Detail != "deployed in 5.7s" {
		t.Fatalf("unexpected apps: %+v", payload.Apps)
	}
}

func TestExtractOutputFlag(t *testing.T) {
	cases := []struct {
		args     []string
		wantJSON bool
		wantRest []string
	}{
		{[]string{"list"}, false, []string{"list"}},
		{[]string{"--json", "list"}, true, []string{"list"}},
		{[]string{"list", "--json"}, true, []string{"list"}},
		{[]string{"--output", "json", "status"}, true, []string{"status"}},
		{[]string{"--output=text", "status"}, false, []string{"status"}},
		{[]string{"doctor", "--output", "json"}, true, []string{"doctor"}},
	}
	for _, tc := range cases {
		gotJSON, gotRest, err := extractOutputFlag(tc.args)
		if err != nil {
			t.Fatalf("%v: unexpected error %v", tc.args, err)
		}
		if gotJSON != tc.wantJSON {
			t.Fatalf("%v: json got %v want %v", tc.args, gotJSON, tc.wantJSON)
		}
		if strings.Join(gotRest, " ") != strings.Join(tc.wantRest, " ") {
			t.Fatalf("%v: rest got %v want %v", tc.args, gotRest, tc.wantRest)
		}
	}

	if _, _, err := extractOutputFlag([]string{"--output", "yaml"}); err == nil {
		t.Fatal("expected error for unknown --output value")
	}
}

func TestOutputListShowsTunnelColumn(t *testing.T) {
	var buf bytes.Buffer
	o := newTextOutput(&buf)
	o.listApps([]AppView{
		{Name: "cadim", Repo: "macecchi/cadim", State: "running", Hosts: []string{"cadim.taileaca57.ts.net"}, Tunnel: "private"},
		{Name: "fullsend", Repo: "dvassallo/fullsend", State: "running", Hosts: []string{"fullsend.game"}, Tunnel: "public"},
	})
	if err := o.Flush(); err != nil {
		t.Fatal(err)
	}
	text := buf.String()
	if !strings.Contains(text, "TUNNEL") {
		t.Fatalf("expected a TUNNEL header:\n%s", text)
	}
	for _, want := range []string{"private", "public"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in the table:\n%s", want, text)
		}
	}
}

func TestOutputStatusShowsTunnelLine(t *testing.T) {
	var buf bytes.Buffer
	o := newTextOutput(&buf)
	o.statusReport(DaemonView{State: "ok", Apps: 1}, []AppView{{
		Name:   "cadim",
		State:  "running",
		Tunnel: "private",
		Deploy: &DeployView{State: "ok", Detail: "deployed in 5.7s"},
	}})
	if err := o.Flush(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "tunnel") || !strings.Contains(buf.String(), "private") {
		t.Fatalf("expected a tunnel line:\n%s", buf.String())
	}
}

func TestOutputStatusShowsAppAndFunnelURLs(t *testing.T) {
	var buf bytes.Buffer
	o := newTextOutput(&buf)
	o.statusReport(DaemonView{State: "ok", Apps: 1}, []AppView{{
		Name:   "cadim",
		State:  "running",
		Hosts:  []string{"cadim.corp.ts.net"},
		Tunnel: "private",
		Funnel: "https://cadim-mcp.corp.ts.net/mcp",
	}})
	if err := o.Flush(); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "private  https://cadim.corp.ts.net") {
		t.Fatalf("expected the app URL on the tunnel line:\n%s", out)
	}
	if !strings.Contains(out, "public   https://cadim-mcp.corp.ts.net/mcp") {
		t.Fatalf("expected the funnel URL marked public:\n%s", out)
	}
}

func TestOutputListShowsShortCommit(t *testing.T) {
	var buf bytes.Buffer
	o := newTextOutput(&buf)
	o.listApps([]AppView{
		{Name: "cadim", Repo: "macecchi/cadim", State: "running", Tunnel: "private", Commit: "ac8ddaf778db9a16eee02b1f727906419a46af44"},
		{Name: "idle", Repo: "acme/idle", State: "stopped", Tunnel: "public"},
	})
	if err := o.Flush(); err != nil {
		t.Fatal(err)
	}
	text := buf.String()
	if !strings.Contains(text, "COMMIT") {
		t.Fatalf("expected a COMMIT header:\n%s", text)
	}
	if !strings.Contains(text, "ac8ddaf") {
		t.Fatalf("expected the short commit:\n%s", text)
	}
	if strings.Contains(text, "ac8ddaf778db9a16") {
		t.Fatalf("the table should abbreviate the sha:\n%s", text)
	}
}

func TestOutputJSONKeepsFullCommit(t *testing.T) {
	var buf bytes.Buffer
	o := newJSONOutput(&buf)
	full := "ac8ddaf778db9a16eee02b1f727906419a46af44"
	o.listApps([]AppView{{Name: "cadim", State: "running", Commit: full}})
	if err := o.Flush(); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Apps []AppView `json:"apps"`
	}
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, buf.String())
	}
	if len(payload.Apps) != 1 || payload.Apps[0].Commit != full {
		t.Fatalf("json should carry the full sha: %+v", payload.Apps)
	}
}
