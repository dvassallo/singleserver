package singleserver

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"runtime/debug"
	"strings"
	"time"
)

var (
	Version   = "dev"
	Commit    = ""
	BuildDate = ""
)

func RunCLI(args []string, logger *log.Logger) error {
	jsonOut, args, err := extractOutputFlag(args)
	if err != nil {
		return err
	}

	enableColorForStdout()
	var out *Output
	if jsonOut {
		useColor = false
		out = newJSONOutput(os.Stdout)
		// JSON output implies machine use; never block on a prompt.
		args = append([]string{"--non-interactive"}, args...)
	} else {
		out = newTextOutput(os.Stdout)
	}

	runErr := runCLI(args, logger, out)
	if flushErr := out.Flush(); runErr == nil && flushErr != nil {
		runErr = flushErr
	}
	return runErr
}

// extractOutputFlag pulls a global --output json|text (or --json) from anywhere
// in the args so it can precede or follow the subcommand, and returns the
// remaining args with it removed.
func extractOutputFlag(args []string) (bool, []string, error) {
	jsonOut := false
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			rest = append(rest, args[i:]...)
			return jsonOut, rest, nil
		case arg == "--json":
			jsonOut = true
		case arg == "--output":
			if i+1 >= len(args) {
				return false, nil, errors.New("--output needs a value: json or text")
			}
			i++
			v, err := parseOutputValue(args[i])
			if err != nil {
				return false, nil, err
			}
			jsonOut = v
		case strings.HasPrefix(arg, "--output="):
			v, err := parseOutputValue(strings.TrimPrefix(arg, "--output="))
			if err != nil {
				return false, nil, err
			}
			jsonOut = v
		default:
			rest = append(rest, arg)
		}
	}
	return jsonOut, rest, nil
}

func parseOutputValue(v string) (bool, error) {
	switch v {
	case "json":
		return true, nil
	case "text":
		return false, nil
	default:
		return false, fmt.Errorf("unknown --output value %q: use json or text", v)
	}
}

func runCLI(args []string, logger *log.Logger, stdout io.Writer) error {
	mode, args, err := parseRootCLIMode(args)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return renderHelp(stdout, nil)
	}

	return withCLIMode(mode, func() error {
		if path, ok := helpRequest(args); ok {
			return renderHelp(stdout, path)
		}
		name := args[0]
		if name == "--version" {
			name = "version"
		}
		cmd := lookupCommand(name)
		if cmd == nil {
			return fmt.Errorf("unknown command %q", name)
		}
		return cmd.Run(args[1:], stdout, logger)
	})
}

// writeCheck records a check. When the writer is an *Output (the production
// path) it stores a structured record for the text or JSON renderer. For a
// plain writer it falls back to the tab-delimited form, which keeps direct
// callers and tests simple.
func writeCheck(w io.Writer, scope string, check string, status string, value string, details ...string) {
	if o, ok := w.(*Output); ok {
		o.addCheck(scope, check, status, value, details...)
		return
	}
	value = valueOrDash(value)
	detail := strings.Join(nonEmptyStrings(details...), " ")
	if detail == "" {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", scope, check, status, value)
		return
	}
	fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", scope, check, status, value, detail)
}

func nonEmptyStrings(values ...string) []string {
	nonEmpty := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			nonEmpty = append(nonEmpty, value)
		}
	}
	return nonEmpty
}

func printVersion(w io.Writer) error {
	version := Version
	commit := Commit
	buildDate := BuildDate

	if info, ok := debug.ReadBuildInfo(); ok {
		if version == "" || version == "dev" {
			if info.Main.Version != "" && info.Main.Version != "(devel)" {
				version = info.Main.Version
			}
		}
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				if commit == "" {
					commit = setting.Value
				}
			case "vcs.time":
				if buildDate == "" {
					buildDate = setting.Value
				}
			}
		}
	}

	if strings.TrimSpace(version) == "" {
		version = "dev"
	}
	if strings.TrimSpace(commit) == "" {
		commit = "unknown"
	} else {
		commit = shortSHA(commit)
	}
	if strings.TrimSpace(buildDate) == "" {
		buildDate = "unknown"
	}
	out, owned := asOutput(w)
	out.versionInfo(VersionView{Version: version, Commit: commit, Built: buildDate})
	if owned {
		return out.Flush()
	}
	return nil
}

func cliList(w io.Writer) error {
	config, err := LoadConfig(envDefault("SINGLESERVER_CONFIG", "/etc/singleserver/apps.yml"))
	if err != nil {
		return err
	}
	out, owned := asOutput(w)
	if len(config.Apps) == 0 {
		out.listApps(nil)
		return flushIfOwned(out, owned)
	}
	containers, containerErr := runningAppContainers()
	journal, _ := recentSingleServerJournal()
	views := make([]AppView, 0, len(config.Apps))
	for _, app := range config.Apps {
		views = append(views, AppView{
			Name:   app.Name,
			Repo:   app.Repo,
			Branch: app.Branch,
			Hosts:  app.Hosts,
			State:  listStateWord(appSummaryStatus(app, containers, containerErr, journal)),
		})
	}
	out.listApps(views)
	return flushIfOwned(out, owned)
}

// listStateWord collapses the richer summary status into the four words the
// list column shows, so the column reads consistently across apps.
func listStateWord(summary string) string {
	switch summary {
	case "ok", "running":
		return "running"
	case "stopped":
		return "stopped"
	case "failed":
		return "failed"
	default:
		return "unknown"
	}
}

func cliStatus(w io.Writer) error {
	config, err := LoadConfig(envDefault("SINGLESERVER_CONFIG", "/etc/singleserver/apps.yml"))
	if err != nil {
		return err
	}
	out, owned := asOutput(w)

	daemon := DaemonView{State: "ok", Apps: len(config.Apps)}
	port := envDefault("SINGLESERVER_PORT", "8787")
	if res, derr := http.Get("http://127.0.0.1:" + port + "/health"); derr != nil {
		daemon.State = "unreachable"
	} else {
		_ = res.Body.Close()
	}

	var views []AppView
	if len(config.Apps) > 0 {
		containers, containerErr := runningAppContainers()
		journal, _ := recentSingleServerJournal()
		views = make([]AppView, 0, len(config.Apps))
		for _, app := range config.Apps {
			depState, depDetail := lastDeployStatusFromJournal(app.Name, journal)
			views = append(views, AppView{
				Name:   app.Name,
				State:  appRuntimeWord(app, containers, containerErr),
				Deploy: deployView(depState, depDetail),
				Health: healthView(app),
			})
		}
	}
	out.statusReport(daemon, views)
	return flushIfOwned(out, owned)
}

func flushIfOwned(out *Output, owned bool) error {
	if owned {
		return out.Flush()
	}
	return nil
}

// appRuntimeWord reports whether the app's container is up, for the status
// header line. The full container name is intentionally omitted as noise.
func appRuntimeWord(app AppConfig, containers map[string]string, err error) string {
	if err != nil {
		return "unknown"
	}
	if _, ok := containerForApp(app.Name, containers); ok {
		return "running"
	}
	return "stopped"
}

func deployView(state, detail string) *DeployView {
	switch state {
	case "ok":
		if ms := parseTotalMS(detail); ms > 0 {
			return &DeployView{State: "ok", Detail: "deployed in " + humanMS(ms)}
		}
		return &DeployView{State: "ok", Detail: "deployed"}
	case "failed":
		return &DeployView{State: "failed", Detail: "last deploy failed"}
	default:
		return &DeployView{State: "none", Detail: "no recent deploy"}
	}
}

func healthView(app AppConfig) *HealthView {
	if strings.TrimSpace(app.Healthcheck) == "" {
		return &HealthView{State: "none"}
	}
	if err := checkURL(app.Healthcheck); err != nil {
		return &HealthView{State: "failed", URL: trimScheme(app.Healthcheck), Error: err.Error()}
	}
	return &HealthView{State: "ok", URL: trimScheme(app.Healthcheck)}
}

func appSummaryStatus(app AppConfig, containers map[string]string, containerErr error, journal string) string {
	if app.Healthcheck != "" {
		if err := checkURL(app.Healthcheck); err != nil {
			return "failed"
		}
		return "ok"
	}
	if status, _ := lastDeployStatusFromJournal(app.Name, journal); status == "failed" {
		return "failed"
	}
	runtime := appRuntimeStatus(app, containers, containerErr)
	switch {
	case strings.HasPrefix(runtime, "running:"):
		return "running"
	case runtime == "stopped":
		return "stopped"
	default:
		return "unknown"
	}
}

func displayBranch(app AppConfig) string {
	if strings.TrimSpace(app.Branch) == "" {
		return "(repo default)"
	}
	return app.Branch
}

func displayHosts(app AppConfig) string {
	if len(app.Hosts) == 0 {
		return "-"
	}
	return strings.Join(app.Hosts, ",")
}

func displayHealthcheck(app AppConfig) string {
	if strings.TrimSpace(app.Healthcheck) == "" {
		return "assumed"
	}
	return app.Healthcheck
}

func appRuntimeStatus(app AppConfig, containers map[string]string, err error) string {
	if err != nil {
		return "unknown:" + compactWhitespace(err.Error())
	}
	if container, ok := containerForApp(app.Name, containers); ok {
		return "running:" + container
	}
	return "stopped"
}

func cliDeploy(args []string, w io.Writer, logger *log.Logger) error {
	mode, args, err := commandModeFromArgs(args, noFlagValues)
	if err != nil {
		return err
	}
	if len(args) > 2 {
		return errors.New("usage: singleserver deploy [owner/repo|app] [ref]")
	}
	prompting := cliCanPrompt(mode)
	if len(args) == 0 && !prompting {
		return errors.New("usage: singleserver deploy <owner/repo|app> [ref]")
	}
	target := ""
	if len(args) >= 1 {
		target = strings.TrimSpace(args[0])
	}
	if target == "" && prompting {
		target, err = promptConfiguredAppName(interactivePrompter(w), "App to deploy")
		if err != nil {
			return err
		}
	}
	if target == "" {
		return errors.New("usage: singleserver deploy <owner/repo|app> [ref]")
	}
	ref := ""
	if len(args) == 2 {
		ref = strings.TrimSpace(args[1])
	}

	app, err := configuredApp(target)
	if err != nil {
		return err
	}
	repo := app.Repo

	github := NewGitHubClient(envDefault("SINGLESERVER_STATE_DIR", "/etc/singleserver"))
	installationID, err := github.RepositoryInstallationID(repo)
	if err != nil {
		return err
	}
	token, err := github.DeployToken(installationID)
	if err != nil {
		return err
	}
	if ref == "" {
		ref = app.Branch
	}
	if ref == "" {
		defaultBranch, err := github.RepositoryDefaultBranch(repo, token)
		if err != nil {
			return err
		}
		ref = defaultBranch
	}
	sha, err := github.CommitSHA(repo, ref, token)
	if err != nil {
		return err
	}

	manager := NewDeployManager(logger, github)
	timing, err := manager.run(DeployRequest{
		App:            *app,
		Repo:           repo,
		Branch:         ref,
		SHA:            sha,
		InstallationID: installationID,
		RunID:          fmt.Sprintf("%s-manual-%d", app.Name, time.Now().UnixMilli()),
	})
	if err != nil {
		return err
	}
	writeCheck(w, app.Name, "deploy", "ok", fmt.Sprintf("%dms", timing.TotalMS), fmt.Sprintf("ref=%s", ref), fmt.Sprintf("sha=%s", shortSHA(sha)))
	if app.Healthcheck != "" {
		writeCheck(w, app.Name, "healthcheck", "ok", app.Healthcheck)
	} else {
		writeCheck(w, app.Name, "healthcheck", "assumed", "-", "no external healthcheck configured")
	}
	if liveURL := appLiveURL(*app); liveURL != "" {
		writeCheck(w, app.Name, "live", "ok", liveURL)
	}
	return nil
}

func shortSHA(sha string) string {
	sha = strings.TrimSpace(sha)
	if len(sha) <= 12 {
		return sha
	}
	return sha[:12]
}

func appLiveURL(app AppConfig) string {
	if len(app.Hosts) == 0 {
		return ""
	}
	return "https://" + app.Hosts[0]
}

func cliInspect(args []string, w io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: singleserver inspect <owner/repo|app>")
	}
	target := strings.TrimSpace(args[0])
	if target == "" {
		return errors.New("usage: singleserver inspect <owner/repo|app>")
	}
	app, err := configuredApp(target)
	if err != nil {
		return err
	}
	renderApp, err := appWithServerSecrets(*app)
	if err != nil {
		return err
	}
	body, err := GeneratedDeployYAML(renderApp)
	if err != nil {
		return err
	}
	_, err = rawWriter(w).Write(body)
	return err
}

func cliLogs(args []string, w io.Writer) error {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	fs.SetOutput(w)
	follow := fs.Bool("follow", false, "follow logs")
	runtimeLogs := fs.Bool("runtime", false, "show app container logs")
	daemonLogs := fs.Bool("daemon", false, "show full Single Server daemon journal")
	if err := fs.Parse(normalizeFlagArgs(args, noFlagValues)); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return errors.New("usage: singleserver logs [app] [--follow] [--runtime] [--daemon]")
	}
	if *runtimeLogs && *daemonLogs {
		return errors.New("usage: singleserver logs [app] [--follow] [--runtime] [--daemon]")
	}

	w = rawWriter(w)
	filter := ""
	if fs.NArg() == 1 {
		filter = strings.TrimSpace(fs.Arg(0))
	}
	if *runtimeLogs {
		if filter == "" {
			return errors.New("usage: singleserver logs <app> --runtime")
		}
		app, err := configuredApp(filter)
		if err != nil {
			return err
		}
		container, err := appContainerName(app.Name)
		if err != nil {
			return err
		}
		logArgs := []string{"logs", "--tail", "200"}
		if *follow {
			logArgs = append(logArgs, "-f")
		}
		logArgs = append(logArgs, container)
		return runCommandToWriter(w, 0, "docker", logArgs...)
	}

	logAppName := ""
	if filter != "" {
		app, err := configuredApp(filter)
		if err != nil {
			return err
		}
		logAppName = app.Name
	}

	journalArgs := []string{"-u", "singleserver.service", "-n", "200", "--no-pager", "-o", "short-iso"}
	if *follow {
		journalArgs = append(journalArgs, "-f")
	}
	if *follow {
		grep := journalLogNeedle(logAppName, *daemonLogs)
		if grep == "" {
			return runCommandToWriter(w, 0, "journalctl", journalArgs...)
		}
		script := "journalctl -u singleserver.service -n 200 --no-pager -o short-iso -f | grep --line-buffered -F " + shellQuote(grep)
		return runCommandToWriter(w, 0, "bash", "-lc", script)
	}
	out, err := commandOutputFunc(5*time.Second, "journalctl", journalArgs...)
	if err != nil {
		return err
	}
	for _, line := range filterJournalLogLines(string(out), logAppName, *daemonLogs) {
		fmt.Fprintln(w, line)
	}
	return nil
}

func journalLogNeedle(appName string, daemonLogs bool) string {
	if daemonLogs {
		return appName
	}
	if appName == "" {
		return "[deploy:"
	}
	return "[deploy:" + appName + "-"
}

func filterJournalLogLines(journal string, appName string, daemonLogs bool) []string {
	needle := journalLogNeedle(appName, daemonLogs)
	lines := []string{}
	for _, line := range strings.Split(journal, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		if needle == "" || strings.Contains(line, needle) {
			lines = append(lines, line)
		}
	}
	return lines
}

func checkURL(url string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 400 {
		return fmt.Errorf("%s returned %d", url, res.StatusCode)
	}
	return nil
}
