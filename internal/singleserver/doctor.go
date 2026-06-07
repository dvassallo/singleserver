package singleserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func cliDoctor(w io.Writer) error {
	failed := false
	if !doctorDaemon(w) {
		failed = true
	}

	configPath := envDefault("SINGLESERVER_CONFIG", "/etc/singleserver/apps.yml")
	config, err := LoadConfig(configPath)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "config\tok\t%s\tapps=%d\n", configPath, len(config.Apps))

	journal, journalErr := recentSingleServerJournal()
	if journalErr != nil {
		fmt.Fprintf(w, "journal\tfailed\t%s\n", journalErr)
		failed = true
	} else {
		fmt.Fprintln(w, "journal\tok")
	}

	github := NewGitHubClient(envDefault("SINGLESERVER_STATE_DIR", "/etc/singleserver"))
	for _, app := range config.Apps {
		fmt.Fprintf(w, "app\t%s\t%s\n", app.Name, app.Repo)
		if !doctorGitHubInstallation(w, github, app) {
			failed = true
		}
		if !doctorDeployConfig(w, app) {
			failed = true
		}
		if !doctorCheckout(w, app) {
			failed = true
		}
		status, detail := lastDeployStatusFromJournal(app.Name, journal)
		fmt.Fprintf(w, "%s\tlast_deploy\t%s\t%s\n", app.Name, status, detail)
		if status == "failed" {
			failed = true
		}
		if !doctorHealthcheck(w, app) {
			failed = true
		}
	}

	if failed {
		return errors.New("doctor found failed checks")
	}
	return nil
}

func doctorDaemon(w io.Writer) bool {
	port := envDefault("SINGLESERVER_PORT", "8787")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+port+"/health", nil)
	if err != nil {
		fmt.Fprintf(w, "daemon\tfailed\t%s\n", err)
		return false
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintf(w, "daemon\tfailed\t%s\n", err)
		return false
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 400 {
		fmt.Fprintf(w, "daemon\tfailed\t%s\n", res.Status)
		return false
	}
	fmt.Fprintf(w, "daemon\tok\t%s\n", res.Status)
	return true
}

func doctorGitHubInstallation(w io.Writer, github *GitHubClient, app AppConfig) bool {
	installationID, err := github.RepositoryInstallationID(app.Repo)
	if err != nil {
		fmt.Fprintf(w, "%s\tgithub_installation\tfailed\t%s\n", app.Name, err)
		return false
	}
	fmt.Fprintf(w, "%s\tgithub_installation\tok\tid=%d\n", app.Name, installationID)
	return true
}

func doctorDeployConfig(w io.Writer, app AppConfig) bool {
	if _, err := GeneratedDeployYAML(app); err != nil {
		fmt.Fprintf(w, "%s\tdeploy_config\tfailed\t%s\n", app.Name, err)
		return false
	}

	if _, err := os.Stat(filepath.Join(app.RepoDir, ".git")); err == nil {
		if err := gitRun(app.RepoDir, "ls-files", "--error-unmatch", "config/deploy.yml"); err == nil {
			fmt.Fprintf(w, "%s\tdeploy_config\tok\trepo config/deploy.yml\n", app.Name)
			return true
		}
	}
	fmt.Fprintf(w, "%s\tdeploy_config\tok\tgenerated from conventions\n", app.Name)
	return true
}

func doctorCheckout(w io.Writer, app AppConfig) bool {
	if _, err := os.Stat(filepath.Join(app.RepoDir, ".git")); err != nil {
		fmt.Fprintf(w, "%s\tcheckout\tfailed\tmissing %s\n", app.Name, app.RepoDir)
		return false
	}

	branch, err := gitOutput(app.RepoDir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		fmt.Fprintf(w, "%s\tcheckout\tfailed\t%s\n", app.Name, err)
		return false
	}
	sha, err := gitOutput(app.RepoDir, "rev-parse", "--short", "HEAD")
	if err != nil {
		fmt.Fprintf(w, "%s\tcheckout\tfailed\t%s\n", app.Name, err)
		return false
	}
	status, err := gitOutput(app.RepoDir, "status", "--short")
	if err != nil {
		fmt.Fprintf(w, "%s\tcheckout\tfailed\t%s\n", app.Name, err)
		return false
	}
	if status != "" {
		fmt.Fprintf(w, "%s\tcheckout\tfailed\t%s branch=%s sha=%s dirty=%q\n", app.Name, app.RepoDir, branch, sha, compactWhitespace(status))
		return false
	}
	fmt.Fprintf(w, "%s\tcheckout\tok\t%s branch=%s sha=%s clean\n", app.Name, app.RepoDir, branch, sha)
	return true
}

func doctorHealthcheck(w io.Writer, app AppConfig) bool {
	if app.Healthcheck == "" {
		fmt.Fprintf(w, "%s\thealthcheck\tunknown\tno healthcheck configured\n", app.Name)
		return true
	}
	if err := checkURL(app.Healthcheck); err != nil {
		fmt.Fprintf(w, "%s\thealthcheck\tfailed\t%s\n", app.Name, err)
		return false
	}
	fmt.Fprintf(w, "%s\thealthcheck\tok\t%s\n", app.Name, app.Healthcheck)
	return true
}

func recentSingleServerJournal() (string, error) {
	out, err := commandOutput(5*time.Second, "journalctl", "-u", "singleserver.service", "-n", "1000", "--no-pager", "-o", "cat")
	if err != nil {
		return "", err
	}
	return out, nil
}

func lastDeployStatusFromJournal(appName string, journal string) (string, string) {
	lines := strings.Split(journal, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		if !strings.Contains(line, "[deploy:"+appName+"-") {
			continue
		}
		if index := strings.Index(line, "success total_ms="); index >= 0 {
			return "ok", strings.TrimSpace(line[index:])
		}
		if index := strings.Index(line, "failed after "); index >= 0 {
			return "failed", strings.TrimSpace(line[index:])
		}
	}
	return "unknown", "no recent deploy outcome"
}

func commandRun(timeout time.Duration, name string, args ...string) error {
	_, err := commandOutput(timeout, name, args...)
	return err
}

func gitRun(repoDir string, args ...string) error {
	_, err := gitOutput(repoDir, args...)
	return err
}

func gitOutput(repoDir string, args ...string) (string, error) {
	gitArgs := append([]string{"-c", "safe.directory=" + repoDir, "-C", repoDir}, args...)
	return commandOutput(3*time.Second, "git", gitArgs...)
}

func commandOutput(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	output := strings.TrimSpace(string(out))
	if ctx.Err() == context.DeadlineExceeded {
		return output, fmt.Errorf("%s timed out", name)
	}
	if err != nil {
		if output == "" {
			output = err.Error()
		}
		return output, fmt.Errorf("%s %s failed: %s", name, strings.Join(args, " "), output)
	}
	return output, nil
}

func compactWhitespace(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
