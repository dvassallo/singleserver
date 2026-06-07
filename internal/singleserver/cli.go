package singleserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
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
	case "list":
		return cliList(os.Stdout)
	case "status":
		return cliStatus(os.Stdout)
	case "deploy":
		return cliDeploy(args[1:], logger)
	case "logs":
		return cliLogs(args[1:], os.Stdout)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, `Single Server

Usage:
  singleserver list
  singleserver status
  singleserver deploy <owner/repo> [ref]
  singleserver logs [app]

Commands:
  list      Show configured apps.
  status    Check the local daemon and configured healthchecks.
  deploy    Deploy a configured app immediately.
  logs      Show recent Single Server journal logs, optionally filtered by app.
`)
}

func cliList(w io.Writer) error {
	config, err := LoadConfig(envDefault("SINGLESERVER_CONFIG", "/etc/singleserver/apps.yml"))
	if err != nil {
		return err
	}
	for _, app := range config.Apps {
		branch := app.Branch
		if branch == "" {
			branch = "(repo default)"
		}
		healthcheck := app.Healthcheck
		if healthcheck == "" {
			healthcheck = "-"
		}
		fmt.Fprintf(w, "%s\t%s\tbranch=%s\thealthcheck=%s\n", app.Name, app.Repo, branch, healthcheck)
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
	for _, app := range config.Apps {
		if app.Healthcheck == "" {
			fmt.Fprintf(w, "%s\t%s\t(no healthcheck)\n", app.Name, app.Repo)
			continue
		}
		status := "ok"
		detail := ""
		if err := checkURL(app.Healthcheck); err != nil {
			status = "failed"
			detail = "\t" + err.Error()
		}
		fmt.Fprintf(w, "%s\t%s\t%s%s\n", app.Name, app.Repo, status, detail)
	}
	return nil
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

func cliLogs(args []string, w io.Writer) error {
	if len(args) > 1 {
		return errors.New("usage: singleserver logs [app]")
	}
	filter := ""
	if len(args) == 1 {
		filter = strings.TrimSpace(args[0])
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "journalctl", "-u", "singleserver.service", "-n", "200", "--no-pager", "-o", "short-iso")
	out, err := cmd.Output()
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
