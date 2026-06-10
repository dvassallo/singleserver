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
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

func cliDoctor(args []string, w io.Writer) error {
	if len(args) > 1 {
		return errors.New("usage: singleserver doctor [app]")
	}

	failed := false
	if !doctorDaemon(w) {
		failed = true
	}

	configPath := envDefault("SINGLESERVER_CONFIG", "/etc/singleserver/apps.yml")
	config, err := LoadConfig(configPath)
	if err != nil {
		return err
	}
	apps, err := doctorApps(config.Apps, args)
	if err != nil {
		return err
	}
	writeCheck(w, "config", "apps", "ok", configPath, fmt.Sprintf("apps=%d", len(config.Apps)), fmt.Sprintf("selected=%d", len(apps)))

	journal, journalErr := recentSingleServerJournal()
	if journalErr != nil {
		writeCheck(w, "journal", "status", "failed", journalErr.Error())
		failed = true
	} else {
		writeCheck(w, "journal", "status", "ok", "-")
	}

	if !doctorDocker(w) {
		failed = true
	}
	if !doctorDeployInfrastructure(w) {
		failed = true
	}
	if !doctorDisk(w) {
		failed = true
	}
	if !doctorTailscale(w, len(config.Apps)) {
		failed = true
	}
	if !doctorCloudflare(w, config.Apps, apps) {
		failed = true
	}

	github := NewGitHubClient(envDefault("SINGLESERVER_STATE_DIR", "/etc/singleserver"))
	if !doctorGitHubSetup(w, github, len(config.Apps), expectedGitHubWebhookURL()) {
		failed = true
	}
	for _, app := range apps {
		writeCheck(w, app.Name, "app", "ok", app.Repo)
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
		writeCheck(w, app.Name, "last_deploy", status, detail)
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

func doctorDocker(w io.Writer) bool {
	failed := false
	version, err := commandOutputFunc(5*time.Second, "docker", "info", "--format", "{{.ServerVersion}}")
	if err != nil {
		writeCheck(w, "docker", "server", "failed", err.Error())
		return false
	}
	if _, err := commandOutputFunc(5*time.Second, "docker", "ps", "--format", "{{.Names}}"); err != nil {
		writeCheck(w, "docker", "containers", "failed", err.Error())
		return false
	}
	writeCheck(w, "docker", "server", "ok", version)
	buildxVersion, err := commandOutputFunc(5*time.Second, "docker", "buildx", "version")
	if err != nil {
		writeCheck(w, "docker", "buildx", "failed", "install docker-buildx")
		failed = true
	} else {
		writeCheck(w, "docker", "buildx", "ok", compactWhitespace(buildxVersion))
	}
	return !failed
}

func doctorDeployInfrastructure(w io.Writer) bool {
	failed := false
	if err := commandRunFunc(3*time.Second, "id", "deploy"); err != nil {
		writeCheck(w, "deploy", "user", "failed", err.Error())
		failed = true
	} else {
		writeCheck(w, "deploy", "user", "ok", "deploy")
	}

	groups, err := commandOutputFunc(3*time.Second, "id", "-nG", "deploy")
	if err != nil {
		writeCheck(w, "deploy", "docker_group", "failed", err.Error())
		failed = true
	} else if !hasWord(groups, "docker") {
		writeCheck(w, "deploy", "docker_group", "failed", "deploy", "groups="+compactWhitespace(groups))
		failed = true
	} else {
		writeCheck(w, "deploy", "docker_group", "ok", "deploy", "groups="+compactWhitespace(groups))
	}

	keyPath := "/root/.ssh/id_ed25519"
	if stat, err := os.Stat(keyPath); err != nil {
		writeCheck(w, "deploy", "ssh_key", "failed", err.Error())
		failed = true
	} else if stat.IsDir() {
		writeCheck(w, "deploy", "ssh_key", "failed", keyPath, "is a directory")
		failed = true
	} else {
		writeCheck(w, "deploy", "ssh_key", "ok", keyPath)
	}

	if err := commandRunFunc(5*time.Second, "ssh", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null", "-o", "ConnectTimeout=3", "deploy@127.0.0.1", "true"); err != nil {
		writeCheck(w, "deploy", "ssh", "failed", err.Error())
		failed = true
	} else {
		writeCheck(w, "deploy", "ssh", "ok", "deploy@127.0.0.1")
	}

	status, err := registryHealthStatus()
	if err != nil {
		writeCheck(w, "registry", "status", "failed", err.Error())
		failed = true
	} else {
		writeCheck(w, "registry", "status", "ok", status)
	}
	return !failed
}

func doctorDisk(w io.Writer) bool {
	path := "/srv"
	if _, err := os.Stat(path); err != nil {
		path = "/"
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		writeCheck(w, "disk", "space", "failed", path, err.Error())
		return false
	}
	total := uint64(stat.Blocks) * uint64(stat.Bsize)
	available := uint64(stat.Bavail) * uint64(stat.Bsize)
	availablePercent := 0.0
	if total > 0 {
		availablePercent = float64(available) * 100 / float64(total)
	}
	if available < 1<<30 || availablePercent < 5 {
		writeCheck(w, "disk", "space", "failed", path, "available="+formatBytesGB(available), fmt.Sprintf("available_percent=%.1f", availablePercent))
		return false
	}
	writeCheck(w, "disk", "space", "ok", path, "available="+formatBytesGB(available), fmt.Sprintf("available_percent=%.1f", availablePercent))
	return true
}

func doctorCloudflare(w io.Writer, allApps []AppConfig, selectedApps []AppConfig) bool {
	state, err := loadCloudflareState()
	if err != nil {
		writeCheck(w, "cloudflare", "state", "failed", err.Error())
		return false
	}

	failed := false
	token := cloudflareTokenFromEnvOrState(state)
	cloudflareConfigured := state.TunnelID != "" || token != ""
	if !cloudflareConfigured {
		if appsHaveHosts(selectedApps) {
			writeCheck(w, "cloudflare", "setup", "skipped", "-", "connect Cloudflare with `singleserver cloudflare connect` to verify DNS and tunnel routes")
		} else {
			writeCheck(w, "cloudflare", "setup", "skipped", "-", "no DNS provider configured")
		}
	} else {
		if state.TunnelID == "" {
			writeCheck(w, "cloudflare", "state", "failed", "-", "missing tunnel; run `singleserver cloudflare connect`")
			failed = true
		} else {
			writeCheck(w, "cloudflare", "state", "ok", "mode=tunnel")
		}
	}

	routes := map[string]string{}
	tunnelMode := state.TunnelID != ""
	if tunnelMode {
		for label, path := range map[string]string{
			"credentials": state.CredentialsFile,
			"config":      state.ConfigFile,
		} {
			if path == "" {
				writeCheck(w, "cloudflare", label, "failed", "missing path")
				failed = true
				continue
			}
			if _, err := os.Stat(path); err != nil {
				writeCheck(w, "cloudflare", label, "failed", err.Error())
				failed = true
				continue
			}
			writeCheck(w, "cloudflare", label, "ok", path)
		}

		if err := commandRunFunc(5*time.Second, "systemctl", "is-active", "--quiet", "cloudflared-singleserver.service"); err != nil {
			writeCheck(w, "cloudflare", "service", "failed", err.Error())
			failed = true
		} else {
			writeCheck(w, "cloudflare", "service", "ok", "cloudflared-singleserver.service")
		}

		config, err := readCloudflaredConfig(state.ConfigFile)
		if err != nil {
			writeCheck(w, "cloudflare", "routes", "failed", err.Error())
			failed = true
		} else {
			routes = cloudflaredRoutes(config)
			writeCheck(w, "cloudflare", "routes", "ok", fmt.Sprintf("count=%d", len(routes)))
		}
	}

	cloudflareClient, cloudflareClientOK := doctorCloudflareClient(w, state)
	if !cloudflareClientOK {
		failed = true
	}

	if tunnelMode {
		expectedHosts := expectedCloudflaredHosts(allApps)
		for _, host := range staleCloudflaredHosts(routes, expectedHosts) {
			writeCheck(w, "cloudflare", "stale_route", "failed", host, "service="+routes[host], "not in apps.yml")
			failed = true
		}
	}

	for _, app := range selectedApps {
		for _, host := range app.Hosts {
			if !tunnelMode && !doctorHostResolves(w, app.Name, "dns", host) {
				failed = true
			}
			if cloudflareClient != nil && !doctorCloudflareDNSRecord(w, app.Name, "cloudflare_dns", host, state, cloudflareClient) {
				failed = true
			}
			if !tunnelMode {
				continue
			}
			if service := routes[strings.ToLower(host)]; service == "" {
				writeCheck(w, app.Name, "tunnel_route", "failed", host, "missing from "+state.ConfigFile)
				failed = true
			} else {
				writeCheck(w, app.Name, "tunnel_route", "ok", host, "service="+service)
			}
		}
	}

	return !failed
}

func doctorCloudflareClient(w io.Writer, state *CloudflareState) (*CloudflareClient, bool) {
	token := cloudflareTokenFromEnvOrState(state)
	if token == "" {
		return nil, true
	}
	client, err := newCloudflareClient(token)
	if err != nil {
		writeCheck(w, "cloudflare", "dns_api", "failed", err.Error())
		return nil, false
	}
	return client, true
}

func doctorCloudflareDNSRecord(w io.Writer, scope string, check string, host string, state *CloudflareState, client *CloudflareClient) bool {
	target, err := verifyCloudflareDNSRecordFunc(host, state, client)
	if err != nil {
		writeCheck(w, scope, check, "failed", host, err.Error())
		return false
	}
	writeCheck(w, scope, check, "ok", host, "target="+target)
	return true
}

var githubHookConfigFunc = func(github *GitHubClient) (*GitHubHookConfig, error) {
	return github.HookConfig()
}

func doctorGitHubSetup(w io.Writer, github *GitHubClient, appCount int, expectedWebhookURL string) bool {
	secrets, err := github.LoadSecrets()
	if err != nil {
		status := "pending"
		if appCount > 0 {
			status = "failed"
		}
		writeCheck(w, "github", "setup", status, "run `singleserver github connect`", err.Error())
		return appCount == 0
	}
	if _, err := github.loadPrivateKey(); err != nil {
		status := "pending"
		if appCount > 0 {
			status = "failed"
		}
		writeCheck(w, "github", "setup", status, "private key unavailable", err.Error())
		return appCount == 0
	}
	writeCheck(w, "github", "setup", "ok", fmt.Sprintf("app_id=%d", secrets.AppID), "slug="+valueOrDash(secrets.Slug))
	if expectedWebhookURL != "" {
		config, err := githubHookConfigFunc(github)
		if err != nil {
			writeCheck(w, "github", "webhook", "failed", err.Error())
			return false
		}
		actualWebhookURL := strings.TrimRight(config.URL, "/")
		if actualWebhookURL != expectedWebhookURL {
			writeCheck(w, "github", "webhook", "failed", expectedWebhookURL, "actual="+valueOrDash(actualWebhookURL))
			return false
		}
		writeCheck(w, "github", "webhook", "ok", expectedWebhookURL)
	}
	return true
}

func expectedGitHubWebhookURL() string {
	env, err := loadServiceEnv()
	if err != nil {
		return ""
	}
	publicURL := strings.TrimRight(env["SINGLESERVER_PUBLIC_URL"], "/")
	if publicURL == "" {
		return ""
	}
	return publicURL + "/github/webhook"
}

func doctorApps(apps []AppConfig, args []string) ([]AppConfig, error) {
	if len(args) > 1 {
		return nil, errors.New("usage: singleserver doctor [app]")
	}
	if len(args) == 0 {
		return apps, nil
	}
	filter := strings.TrimSpace(args[0])
	if filter == "" {
		return nil, errors.New("usage: singleserver doctor [app]")
	}
	for _, app := range apps {
		if appMatches(app, filter) {
			return []AppConfig{app}, nil
		}
	}
	return nil, fmt.Errorf("%s is not configured", filter)
}

func doctorDaemon(w io.Writer) bool {
	port := envDefault("SINGLESERVER_PORT", "8787")
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	var lastStatus string
	for {
		status, err := daemonHealthStatus(port)
		if err == nil {
			writeCheck(w, "daemon", "status", "ok", status)
			return true
		}
		lastErr = err
		lastStatus = status
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if lastStatus != "" {
		writeCheck(w, "daemon", "status", "failed", lastStatus)
	} else {
		writeCheck(w, "daemon", "status", "failed", lastErr.Error())
	}
	return false
}

func daemonHealthStatus(port string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+port+"/health", nil)
	if err != nil {
		return "", err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 400 {
		return res.Status, errors.New(res.Status)
	}
	return res.Status, nil
}

func doctorGitHubInstallation(w io.Writer, github *GitHubClient, app AppConfig) bool {
	installationID, err := github.RepositoryInstallationID(app.Repo)
	if err != nil {
		writeCheck(w, app.Name, "github_installation", "failed", err.Error())
		return false
	}
	writeCheck(w, app.Name, "github_installation", "ok", fmt.Sprintf("id=%d", installationID))
	return true
}

func doctorDeployConfig(w io.Writer, app AppConfig) bool {
	renderApp, err := appWithServerSecrets(app)
	if err != nil {
		writeCheck(w, app.Name, "deploy_config", "failed", err.Error())
		return false
	}
	if _, err := GeneratedDeployYAML(renderApp); err != nil {
		writeCheck(w, app.Name, "deploy_config", "failed", err.Error())
		return false
	}

	if _, err := os.Stat(filepath.Join(app.RepoDir, ".git")); err == nil {
		if err := gitRun(app.RepoDir, "ls-files", "--error-unmatch", "config/deploy.yml"); err == nil {
			writeCheck(w, app.Name, "deploy_config", "ok", "repo config/deploy.yml")
			return true
		}
	}
	writeCheck(w, app.Name, "deploy_config", "ok", "generated from conventions")
	return true
}

func doctorCheckout(w io.Writer, app AppConfig) bool {
	if _, err := os.Stat(filepath.Join(app.RepoDir, ".git")); err != nil {
		writeCheck(w, app.Name, "checkout", "failed", app.RepoDir, "missing")
		return false
	}

	branch, err := gitOutput(app.RepoDir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		writeCheck(w, app.Name, "checkout", "failed", err.Error())
		return false
	}
	sha, err := gitOutput(app.RepoDir, "rev-parse", "--short", "HEAD")
	if err != nil {
		writeCheck(w, app.Name, "checkout", "failed", err.Error())
		return false
	}
	status, err := gitOutput(app.RepoDir, "status", "--short")
	if err != nil {
		writeCheck(w, app.Name, "checkout", "failed", err.Error())
		return false
	}
	if status != "" {
		writeCheck(w, app.Name, "checkout", "failed", app.RepoDir, "branch="+branch, "sha="+sha, fmt.Sprintf("dirty=%q", compactWhitespace(status)))
		return false
	}
	writeCheck(w, app.Name, "checkout", "ok", app.RepoDir, "branch="+branch, "sha="+sha, "clean")
	return true
}

func doctorHealthcheck(w io.Writer, app AppConfig) bool {
	if app.Healthcheck == "" {
		writeCheck(w, app.Name, "healthcheck", "assumed", "-", "no external healthcheck configured")
		return true
	}
	if err := checkURL(app.Healthcheck); err != nil {
		writeCheck(w, app.Name, "healthcheck", "failed", app.Healthcheck, err.Error())
		return false
	}
	writeCheck(w, app.Name, "healthcheck", "ok", app.Healthcheck)
	return true
}

func registryHealthStatus() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, envDefault("SINGLESERVER_REGISTRY_HEALTH_URL", "http://127.0.0.1:5555/v2/"), nil)
	if err != nil {
		return "", err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 400 {
		return res.Status, errors.New(res.Status)
	}
	return res.Status, nil
}

func hasWord(value string, word string) bool {
	for _, field := range strings.Fields(value) {
		if field == word {
			return true
		}
	}
	return false
}

func doctorHostResolves(w io.Writer, scope string, check string, host string) bool {
	if err := commandRunFunc(5*time.Second, "getent", "hosts", host); err != nil {
		writeCheck(w, scope, check, "failed", host, err.Error())
		return false
	}
	writeCheck(w, scope, check, "ok", host)
	return true
}

func readCloudflaredConfig(path string) (*cloudflaredConfig, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var config cloudflaredConfig
	if err := yaml.Unmarshal(body, &config); err != nil {
		return nil, err
	}
	return &config, nil
}

func cloudflaredRoutes(config *cloudflaredConfig) map[string]string {
	routes := map[string]string{}
	if config == nil {
		return routes
	}
	for _, ingress := range config.Ingress {
		if ingress.Hostname == "" {
			continue
		}
		routes[strings.ToLower(ingress.Hostname)] = ingress.Service
	}
	return routes
}

func appsHaveHosts(apps []AppConfig) bool {
	for _, app := range apps {
		if len(app.Hosts) > 0 {
			return true
		}
	}
	return false
}

func expectedCloudflaredHosts(apps []AppConfig) map[string]bool {
	hosts := map[string]bool{}
	for _, app := range apps {
		for _, host := range app.Hosts {
			host = strings.TrimSpace(host)
			if host != "" {
				hosts[strings.ToLower(host)] = true
			}
		}
	}
	return hosts
}

func valueOrDash(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	return value
}

func formatBytesGB(value uint64) string {
	return fmt.Sprintf("%.1fGB", float64(value)/(1<<30))
}

func recentSingleServerJournal() (string, error) {
	out, err := commandOutputFunc(5*time.Second, "journalctl", "-u", "singleserver.service", "-n", "1000", "--no-pager", "-o", "cat")
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
	_, err := commandOutputFunc(timeout, name, args...)
	return err
}

var commandRunFunc = commandRun

func gitRun(repoDir string, args ...string) error {
	_, err := gitOutput(repoDir, args...)
	return err
}

func gitOutput(repoDir string, args ...string) (string, error) {
	gitArgs := append([]string{"-c", "safe.directory=" + repoDir, "-C", repoDir}, args...)
	return commandOutputFunc(3*time.Second, "git", gitArgs...)
}

var commandOutputFunc = commandOutput

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
