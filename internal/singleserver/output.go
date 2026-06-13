package singleserver

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Output is the single sink every command writes to. It records structured
// results (checks, app views, version) and renders them as grouped text on a
// terminal or as JSON for --output json. It also implements io.Writer: notes,
// prompts, and streamed command output pass through in text mode and are
// suppressed in JSON mode so the machine output stays clean.
type Output struct {
	w    io.Writer
	json bool

	kind    outKind
	checks  []ReportCheck
	apps    []AppView
	daemon  DaemonView
	version VersionView
	started bool
	helped  bool
}

type outKind int

const (
	kindChecks outKind = iota
	kindList
	kindStatus
	kindVersion
)

type ReportCheck struct {
	Scope  string `json:"scope"`
	Check  string `json:"check"`
	Status string `json:"status"`
	Value  string `json:"value,omitempty"`
}

type DeployView struct {
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
}

type HealthView struct {
	State string `json:"state"`
	URL   string `json:"url,omitempty"`
	Error string `json:"error,omitempty"`
}

type AppView struct {
	Name   string      `json:"name"`
	Repo   string      `json:"repo,omitempty"`
	Branch string      `json:"branch,omitempty"`
	Hosts  []string    `json:"hosts,omitempty"`
	State  string      `json:"state"`
	Deploy *DeployView `json:"deploy,omitempty"`
	Health *HealthView `json:"health,omitempty"`
}

type DaemonView struct {
	State string `json:"state"`
	Apps  int    `json:"apps"`
}

type VersionView struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Built   string `json:"built"`
}

func newTextOutput(w io.Writer) *Output { return &Output{w: w} }
func newJSONOutput(w io.Writer) *Output { return &Output{w: w, json: true} }

// asOutput returns w unchanged if it is already an *Output, otherwise wraps it
// in a text Output. The bool reports whether the caller owns flushing (true for
// a fresh wrapper, false when RunCLI owns the shared Output).
func asOutput(w io.Writer) (*Output, bool) {
	if o, ok := w.(*Output); ok {
		return o, false
	}
	return newTextOutput(w), true
}

func (o *Output) Write(p []byte) (int, error) {
	if o.json {
		return len(p), nil
	}
	o.flushChecks()
	return o.w.Write(p)
}

// rawWriter returns the underlying writer, bypassing buffering and the JSON
// drop. Commands that emit a raw artifact (inspect YAML, log streams) use it so
// their output is unchanged regardless of --output.
func rawWriter(w io.Writer) io.Writer {
	if o, ok := w.(*Output); ok {
		return o.w
	}
	return w
}

func (o *Output) addCheck(scope, check, status, value string, details ...string) {
	if value == "-" {
		value = ""
	}
	combined := strings.TrimSpace(strings.Join(nonEmptyStrings(value, strings.Join(nonEmptyStrings(details...), " ")), " "))
	o.kind = kindChecks
	o.checks = append(o.checks, ReportCheck{Scope: scope, Check: check, Status: status, Value: combined})
}

func (o *Output) listApps(apps []AppView) { o.kind = kindList; o.apps = apps }
func (o *Output) statusReport(d DaemonView, a []AppView) {
	o.kind = kindStatus
	o.daemon = d
	o.apps = a
}
func (o *Output) versionInfo(v VersionView) { o.kind = kindVersion; o.version = v }

func (o *Output) Flush() error {
	// Help renders its own text directly through the raw writer, so there is
	// nothing structured to flush (and no empty JSON object to emit).
	if o.helped {
		return nil
	}
	if o.json {
		return o.renderJSON()
	}
	switch o.kind {
	case kindList:
		o.renderList()
	case kindStatus:
		o.renderStatus()
	case kindVersion:
		o.renderVersion()
	default:
		o.flushChecks()
	}
	return nil
}

func (o *Output) renderJSON() error {
	var payload any
	switch o.kind {
	case kindList:
		payload = map[string]any{"apps": appsForJSON(o.apps)}
	case kindStatus:
		payload = map[string]any{"daemon": o.daemon, "apps": appsForJSON(o.apps)}
	case kindVersion:
		payload = o.version
	default:
		ok := true
		for _, c := range o.checks {
			if c.Status == "failed" {
				ok = false
				break
			}
		}
		payload = map[string]any{"ok": ok, "checks": checksForJSON(o.checks)}
	}
	enc := json.NewEncoder(o.w)
	enc.SetIndent("", "  ")
	return enc.Encode(payload)
}

func appsForJSON(apps []AppView) []AppView {
	if apps == nil {
		return []AppView{}
	}
	return apps
}

func checksForJSON(checks []ReportCheck) []ReportCheck {
	if checks == nil {
		return []ReportCheck{}
	}
	return checks
}

// flushChecks renders any buffered checks as grouped stanzas and clears them.
// It is called incrementally before passthrough writes (so order is preserved)
// and once more at Flush. JSON mode never calls it; checks stay buffered for a
// single array.
func (o *Output) flushChecks() {
	if len(o.checks) == 0 {
		return
	}
	checks := o.checks
	o.checks = nil
	for i := 0; i < len(checks); {
		j := i + 1
		for j < len(checks) && checks[j].Scope == checks[i].Scope {
			j++
		}
		o.renderCheckGroup(checks[i:j])
		i = j
	}
}

func (o *Output) renderCheckGroup(group []ReportCheck) {
	if o.started {
		fmt.Fprintln(o.w)
	}
	o.started = true
	fmt.Fprintln(o.w, bold(group[0].Scope))

	width := 0
	for _, c := range group {
		width = max(width, len(prettyCheck(c.Check)))
	}
	for _, c := range group {
		name := prettyCheck(c.Check)
		var b strings.Builder
		b.WriteString("  ")
		b.WriteString(mark(checkState(c.Status)))
		b.WriteString(" ")
		b.WriteString(name)
		b.WriteString(strings.Repeat(" ", width-len(name)+3))
		if c.Status != "ok" && c.Status != "failed" {
			b.WriteString(dim(c.Status))
			if c.Value != "" {
				b.WriteString(" ")
			}
		}
		b.WriteString(c.Value)
		fmt.Fprintln(o.w, strings.TrimRight(b.String(), " "))
	}
}

func (o *Output) renderList() {
	if len(o.apps) == 0 {
		o.renderNoApps()
		return
	}
	rows := [][]tcell{{
		cell("APP", bold("APP")),
		cell("STATUS", bold("STATUS")),
		cell("DOMAIN", bold("DOMAIN")),
		cell("REPO", bold("REPO")),
	}}
	for _, a := range o.apps {
		st := wordState(a.State)
		rows = append(rows, []tcell{
			plainCell(a.Name),
			cell("● "+a.State, paint(stateColor(st), "● "+a.State)),
			domainCell(a.Hosts),
			repoCell(a.Repo, a.Branch),
		})
	}
	writeTable(o.w, rows, 2)
}

func (o *Output) renderStatus() {
	st := stateOK
	word := "ok"
	if o.daemon.State != "ok" {
		st = stateFail
		word = o.daemon.State
	}
	count := fmt.Sprintf("%d apps", o.daemon.Apps)
	if o.daemon.Apps == 1 {
		count = "1 app"
	}
	fmt.Fprintf(o.w, "%s  %s %s%s\n", dim("daemon"), dot(st), word, dim("    "+count))

	if len(o.apps) == 0 {
		fmt.Fprintln(o.w)
		o.renderNoApps()
		return
	}
	nameWidth := 0
	for _, a := range o.apps {
		nameWidth = max(nameWidth, len(a.Name))
	}
	for _, a := range o.apps {
		fmt.Fprintln(o.w)
		fmt.Fprintf(o.w, "%s %s%s%s\n", dot(wordState(a.State)), bold(a.Name), strings.Repeat(" ", nameWidth-len(a.Name)+3), dim(a.State))
		if a.Deploy != nil {
			fmt.Fprintf(o.w, "    %s   %s %s\n", dim("deploy"), mark(wordState(a.Deploy.State)), a.Deploy.Detail)
		}
		if a.Health != nil {
			fmt.Fprintf(o.w, "    %s   %s %s\n", dim("health"), mark(wordState(a.Health.State)), healthText(a.Health))
		}
	}
}

func (o *Output) renderVersion() {
	fmt.Fprintf(o.w, "singleserver %s\n", bold(o.version.Version))
	fmt.Fprintf(o.w, "%s %s\n", dim("commit"), o.version.Commit)
	fmt.Fprintf(o.w, "%s  %s\n", dim("built"), o.version.Built)
}

func (o *Output) renderNoApps() {
	fmt.Fprintln(o.w, "No apps configured. Add your first one with:")
	fmt.Fprintln(o.w, "  singleserver add https://github.com/owner/repo")
}

func healthText(h *HealthView) string {
	switch h.State {
	case "none":
		return "no external healthcheck"
	case "failed":
		if h.URL != "" {
			return h.URL + " unreachable"
		}
		return h.Error
	default:
		return h.URL
	}
}

func prettyCheck(check string) string {
	return strings.ReplaceAll(check, "_", " ")
}

// checkState maps a check status word to its glyph/color. ok and failed are
// self-evident from the glyph; other states keep their word in the output.
func checkState(status string) stateKind {
	switch status {
	case "ok":
		return stateOK
	case "failed":
		return stateFail
	case "pending", "canceled":
		return stateWarn
	default:
		return stateMuted
	}
}

// wordState maps an app/deploy/health state word to its glyph/color.
func wordState(word string) stateKind {
	switch word {
	case "ok", "running":
		return stateOK
	case "failed":
		return stateFail
	case "stopped":
		return stateWarn
	default:
		return stateMuted
	}
}

func domainCell(hosts []string) tcell {
	if len(hosts) == 0 {
		return cell("–", dim("–"))
	}
	if len(hosts) == 1 {
		return plainCell(hosts[0])
	}
	extra := fmt.Sprintf(" +%d", len(hosts)-1)
	return cell(hosts[0]+extra, hosts[0]+dim(extra))
}

func repoCell(repo, branch string) tcell {
	if strings.TrimSpace(branch) == "" {
		return plainCell(repo)
	}
	suffix := " (" + branch + ")"
	return cell(repo+suffix, repo+dim(suffix))
}

func parseTotalMS(detail string) int64 {
	const key = "total_ms="
	idx := strings.Index(detail, key)
	if idx < 0 {
		return 0
	}
	rest := detail[idx+len(key):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0
	}
	ms := int64(0)
	for _, c := range rest[:end] {
		ms = ms*10 + int64(c-'0')
	}
	return ms
}

func humanMS(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}
