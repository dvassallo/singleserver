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
	"strings"
	"time"
)

func RunCLI(args []string, logger *log.Logger) error {
	if len(args) == 0 {
		return Run(logger)
	}

	switch args[0] {
	case "help", "-h", "--help":
		printUsage(os.Stdout)
		return nil
	case "init":
		return cliInit(args[1:], os.Stdout)
	case "github":
		if len(args) >= 2 && args[1] == "connect" {
			return cliGitHubConnect(args[2:], os.Stdout)
		}
		return errors.New("usage: singleserver github connect [--name \"Single Server\"]")
	case "cloudflare":
		if len(args) >= 2 && args[1] == "connect" {
			return cliCloudflareConnect(args[2:], os.Stdout)
		}
		return errors.New("usage: singleserver cloudflare connect")
	case "list":
		return cliList(os.Stdout)
	case "status":
		return cliStatus(os.Stdout)
	case "add":
		return cliAdd(args[1:], os.Stdout, logger)
	case "deploy":
		return cliDeploy(args[1:], logger)
	case "render-deploy":
		return cliRenderDeploy(args[1:], os.Stdout)
	case "doctor":
		return cliDoctor(args[1:], os.Stdout)
	case "logs":
		return cliLogs(args[1:], os.Stdout)
	case "remove":
		return cliRemove(args[1:], os.Stdout)
	case "domains":
		return cliDomains(args[1:], os.Stdout, logger)
	case "env":
		return cliEnv(args[1:], os.Stdout)
	case "storage":
		return cliStorage(args[1:], os.Stdout)
	case "backup":
		return cliBackup(args[1:], os.Stdout)
	case "restore":
		return cliRestore(args[1:], os.Stdout)
	case "upgrade":
		return cliUpgrade(os.Stdout)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, `Single Server

Usage:
  singleserver init [--zone example.com] [--skip-cloudflare]
  singleserver github connect [--name "Single Server"]
  singleserver cloudflare connect [--zone example.com] [--tunnel singleserver] [--hook-host hooks.example.com]
  singleserver list
  singleserver status
  singleserver add <github-url> [options]
  singleserver deploy <owner/repo> [ref]
  singleserver render-deploy <owner/repo>
  singleserver doctor [app]
  singleserver logs [app] [options]
  singleserver domains <add|remove|list|verify> ...
  singleserver env <set|list|unset> ...
  singleserver storage enable <app> [--mount /storage] [--path /srv/storage/app]
  singleserver backup <app>
  singleserver restore <app> <backup-id-or-path> --yes [--no-restart]
  singleserver remove <app> [--delete-storage --yes]
  singleserver upgrade

Commands:
  init           Create base server state, connect providers when configured, and print GitHub setup URL.
  github         Repair or print the GitHub App setup URL.
  cloudflare     Create or repair the Cloudflare Tunnel and webhook DNS route.
  list           Show configured apps.
  status         Check the local daemon and configured healthchecks.
  add            Add a GitHub repository to apps.yml.
  deploy         Deploy a configured app immediately.
  render-deploy  Print the generated Kamal deploy.yml for a configured app.
  doctor         Check config, GitHub App access, checkouts, deploy logs, and healthchecks.
  logs           Show recent Single Server journal logs, optionally filtered by app.
  domains        Manage app domains in apps.yml.
  env            Manage server-side app environment variables.
  storage        Manage persistent app storage.
  backup         Back up app storage.
  restore        Restore app storage from a backup.
  remove         Remove app config and stop matching containers.
  upgrade        Re-run the installer and restart Single Server.
`)
}

func cliList(w io.Writer) error {
	config, err := LoadConfig(envDefault("SINGLESERVER_CONFIG", "/etc/singleserver/apps.yml"))
	if err != nil {
		return err
	}
	for _, app := range config.Apps {
		fmt.Fprintf(w, "%s\t%s\tbranch=%s\thosts=%s\thealthcheck=%s\n", app.Name, app.Repo, displayBranch(app), displayHosts(app), displayHealthcheck(app))
	}
	return nil
}

func cliStatus(w io.Writer) error {
	port := envDefault("SINGLESERVER_PORT", "8787")
	res, err := http.Get("http://127.0.0.1:" + port + "/health")
	if err != nil {
		fmt.Fprintf(w, "daemon\tfailed\t%s\n", err)
	} else {
		_ = res.Body.Close()
		fmt.Fprintf(w, "daemon\t%s\n", res.Status)
	}

	config, err := LoadConfig(envDefault("SINGLESERVER_CONFIG", "/etc/singleserver/apps.yml"))
	if err != nil {
		return err
	}
	containers, containerErr := runningAppContainers()
	for _, app := range config.Apps {
		runtime := appRuntimeStatus(app, containers, containerErr)
		prefix := fmt.Sprintf("%s\t%s\tbranch=%s\thosts=%s\truntime=%s", app.Name, app.Repo, displayBranch(app), displayHosts(app), runtime)
		if app.Healthcheck == "" {
			fmt.Fprintf(w, "%s\thealthcheck=unknown\tno healthcheck configured\n", prefix)
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
		return "-"
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

func cliDeploy(args []string, logger *log.Logger) error {
	if len(args) < 1 || len(args) > 2 {
		return errors.New("usage: singleserver deploy <owner/repo> [ref]")
	}
	repo := strings.TrimSpace(args[0])
	ref := ""
	if len(args) == 2 {
		ref = strings.TrimSpace(args[1])
	}

	config, err := LoadConfig(envDefault("SINGLESERVER_CONFIG", "/etc/singleserver/apps.yml"))
	if err != nil {
		return err
	}
	app, ok := config.AppByRepo(repo)
	if !ok {
		return fmt.Errorf("%s is not configured", repo)
	}

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
	return manager.run(DeployRequest{
		App:            *app,
		Repo:           repo,
		Branch:         ref,
		SHA:            sha,
		InstallationID: installationID,
		RunID:          fmt.Sprintf("%s-manual-%d", app.Name, time.Now().UnixMilli()),
	})
}

func cliRenderDeploy(args []string, w io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: singleserver render-deploy <owner/repo>")
	}
	repo := strings.TrimSpace(args[0])

	config, err := LoadConfig(envDefault("SINGLESERVER_CONFIG", "/etc/singleserver/apps.yml"))
	if err != nil {
		return err
	}
	app, ok := config.AppByRepo(repo)
	if !ok {
		return fmt.Errorf("%s is not configured", repo)
	}
	keys, err := appSecretKeys(app.Name)
	if err != nil {
		return err
	}
	app.SecretEnvKeys = keys
	body, err := GeneratedDeployYAML(*app)
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
	if err := fs.Parse(normalizeFlagArgs(args, noFlagValues)); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return errors.New("usage: singleserver logs [app] [--follow] [--runtime]")
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

	journalArgs := []string{"-u", "singleserver.service", "-n", "200", "--no-pager", "-o", "short-iso"}
	if *follow {
		journalArgs = append(journalArgs, "-f")
	}
	if *follow {
		if filter == "" {
			return runCommandToWriter(w, 0, "journalctl", journalArgs...)
		}
		script := "journalctl -u singleserver.service -n 200 --no-pager -o short-iso -f | grep --line-buffered -F " + shellQuote(filter)
		return runCommandToWriter(w, 0, "bash", "-lc", script)
	}
	out, err := commandOutput(5*time.Second, "journalctl", journalArgs...)
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(out), "\n") {
		if filter == "" || strings.Contains(line, filter) {
			fmt.Fprintln(w, line)
		}
	}
	return nil
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
