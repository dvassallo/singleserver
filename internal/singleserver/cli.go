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
	"text/tabwriter"
	"time"
)

var (
	Version   = "dev"
	Commit    = ""
	BuildDate = ""
)

func RunCLI(args []string, logger *log.Logger) error {
	if len(args) == 0 {
		return Run(logger)
	}

	out := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	err := runCLI(args, logger, out)
	if flushErr := out.Flush(); err == nil && flushErr != nil {
		err = flushErr
	}
	return err
}

func runCLI(args []string, logger *log.Logger, stdout io.Writer) error {

	switch args[0] {
	case "help", "-h", "--help":
		printUsage(stdout)
		return nil
	case "version", "--version":
		printVersion(stdout)
		return nil
	case "github":
		if len(args) >= 2 && args[1] == "connect" {
			return cliGitHubConnect(args[2:], stdout)
		}
		return errors.New("usage: singleserver github connect")
	case "tailscale":
		if len(args) >= 2 && args[1] == "connect" {
			return cliTailscaleConnect(args[2:], stdout)
		}
		return errors.New("usage: singleserver tailscale connect")
	case "cloudflare":
		if len(args) >= 2 && args[1] == "connect" {
			return cliCloudflareConnect(args[2:], stdout)
		}
		return errors.New("usage: singleserver cloudflare connect")
	case "list":
		return cliList(stdout)
	case "status":
		return cliStatus(stdout)
	case "add":
		return cliAdd(args[1:], stdout, logger)
	case "edit":
		return cliEdit(args[1:], stdout, logger)
	case "deploy":
		return cliDeploy(args[1:], stdout, logger)
	case "render-deploy":
		return cliRenderDeploy(args[1:], stdout)
	case "doctor":
		return cliDoctor(args[1:], stdout)
	case "logs":
		return cliLogs(args[1:], stdout)
	case "remove":
		return cliRemove(args[1:], stdout)
	case "domains":
		return cliDomains(args[1:], stdout, logger)
	case "env":
		return cliEnv(args[1:], stdout)
	case "storage":
		return cliStorage(args[1:], stdout, logger)
	case "backup":
		return cliBackup(args[1:], stdout)
	case "restore":
		return cliRestore(args[1:], stdout)
	case "upgrade":
		return cliUpgrade(stdout)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func writeCheck(w io.Writer, scope string, check string, status string, value string, details ...string) {
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

func printUsage(w io.Writer) {
	fmt.Fprint(w, `Single Server

Usage:
  singleserver version
  singleserver tailscale connect [--auth-key <key>] [--hostname <name>]
  singleserver github connect
  singleserver cloudflare connect [--account <id>] [--tunnel <name>]
  singleserver list
  singleserver status
  singleserver add <github-url> [options] [--yes]
  singleserver edit <app|owner/repo|github-url> [options]
  singleserver deploy <owner/repo|app> [ref]
  singleserver render-deploy <owner/repo|app>
  singleserver doctor [app]
  singleserver logs [app] [options]
  singleserver domains <add|remove|list|verify> ...
  singleserver env <set|list|unset> ...
  singleserver storage enable <app> [--mount /storage] [--path /srv/storage/app] [--no-deploy]
  singleserver backup <app>
  singleserver restore <app> <backup-id-or-path> --yes [--no-restart]
  singleserver remove <app> [--delete-storage] [--delete-repo] [--yes]
  singleserver upgrade

Commands:
  version        Print the installed Single Server version.
  tailscale      Connect Tailscale SSH and Funnel for setup/webhooks.
  github         Repair or print the GitHub App setup URL.
  cloudflare     Connect Cloudflare Tunnel and DNS for public app ingress.
  list           Show configured apps.
  status         Check the local daemon, apps, and optional healthchecks.
  add            Add and deploy a GitHub repository.
  edit           Edit app config interactively or with flags.
  deploy         Deploy a configured app immediately.
  render-deploy  Print the generated Kamal deploy.yml for a configured app.
  doctor         Check config, deploy plumbing, GitHub App access, checkouts, deploy logs, and optional healthchecks.
  logs           Show recent deploy logs, optionally filtered by app.
  domains        Manage app domains in apps.yml.
  env            Manage server-side app environment variables.
  storage        Manage persistent app storage.
  backup         Back up app storage.
  restore        Restore app storage from a backup.
  remove         Remove app config and stop matching containers.
  upgrade        Re-run the installer and restart Single Server.
`)
}

func printVersion(w io.Writer) {
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
	fmt.Fprintf(w, "singleserver\tversion\t%s\tcommit=%s\tbuilt=%s\n", version, commit, buildDate)
}

func cliList(w io.Writer) error {
	configPath := envDefault("SINGLESERVER_CONFIG", "/etc/singleserver/apps.yml")
	config, err := LoadConfig(configPath)
	if err != nil {
		return err
	}
	if len(config.Apps) == 0 {
		printNoApps(w)
		return nil
	}
	containers, containerErr := runningAppContainers()
	journal, _ := recentSingleServerJournal()
	for _, app := range config.Apps {
		fmt.Fprintf(w, "%s\t%s\tbranch=%s\thosts=%s\tstatus=%s\thealthcheck=%s\n", app.Name, app.Repo, displayBranch(app), displayHosts(app), appSummaryStatus(app, containers, containerErr, journal), displayHealthcheck(app))
	}
	return nil
}

func cliStatus(w io.Writer) error {
	port := envDefault("SINGLESERVER_PORT", "8787")
	res, err := http.Get("http://127.0.0.1:" + port + "/health")
	if err != nil {
		writeCheck(w, "daemon", "status", "failed", err.Error())
	} else {
		_ = res.Body.Close()
		writeCheck(w, "daemon", "status", "ok", res.Status)
	}

	configPath := envDefault("SINGLESERVER_CONFIG", "/etc/singleserver/apps.yml")
	config, err := LoadConfig(configPath)
	if err != nil {
		return err
	}
	writeCheck(w, "config", "apps", "ok", configPath, fmt.Sprintf("apps=%d", len(config.Apps)))
	if len(config.Apps) == 0 {
		printNoApps(w)
		return nil
	}
	containers, containerErr := runningAppContainers()
	journal, _ := recentSingleServerJournal()
	for _, app := range config.Apps {
		runtime := appRuntimeStatus(app, containers, containerErr)
		lastDeploy, lastDeployDetail := lastDeployStatusFromJournal(app.Name, journal)
		prefix := fmt.Sprintf("%s\t%s\tbranch=%s\thosts=%s\tstatus=%s\truntime=%s\tlast_deploy=%s", app.Name, app.Repo, displayBranch(app), displayHosts(app), appSummaryStatus(app, containers, containerErr, journal), runtime, lastDeploy)
		if lastDeployDetail != "" {
			prefix += "\t" + lastDeployDetail
		}
		if app.Healthcheck == "" {
			fmt.Fprintf(w, "%s\thealthcheck=assumed\tno external healthcheck configured\n", prefix)
			continue
		}
		status := "ok"
		detail := ""
		if err := checkURL(app.Healthcheck); err != nil {
			status = "failed"
			detail = "\t" + err.Error()
		}
		fmt.Fprintf(w, "%s\thealthcheck=%s%s\n", prefix, status, detail)
	}
	return nil
}

func printNoApps(w io.Writer) {
	writeCheck(w, "apps", "count", "ok", "0", "add your first app with `singleserver add https://github.com/owner/repo`")
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
	if len(args) < 1 || len(args) > 2 {
		return errors.New("usage: singleserver deploy <owner/repo|app> [ref]")
	}
	target := strings.TrimSpace(args[0])
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

func cliRenderDeploy(args []string, w io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: singleserver render-deploy <owner/repo|app>")
	}
	target := strings.TrimSpace(args[0])
	if target == "" {
		return errors.New("usage: singleserver render-deploy <owner/repo|app>")
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
	_, err = w.Write(body)
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
