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
	fmt.Fprintf(w, "config\tok\t%s\tapps=%d\tselected=%d\n", configPath, len(config.Apps), len(apps))

	journal, journalErr := recentSingleServerJournal()
	if journalErr != nil {
		fmt.Fprintf(w, "journal\tfailed\t%s\n", journalErr)
		failed = true
	} else {
		fmt.Fprintln(w, "journal\tok")
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
	if !doctorCloudflare(w, config.Apps, apps) {
		failed = true
	}

	github := NewGitHubClient(envDefault("SINGLESERVER_STATE_DIR", "/etc/singleserver"))
	if !doctorGitHubSetup(w, github, len(config.Apps)) {
		failed = true
	}
	for _, app := range apps {
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

func doctorDocker(w io.Writer) bool {
	version, err := commandOutputFunc(5*time.Second, "docker", "info", "--format", "{{.ServerVersion}}")
	if err != nil {
		fmt.Fprintf(w, "docker\tfailed\t%s\n", err)
		return false
	}
	if _, err := commandOutputFunc(5*time.Second, "docker", "ps", "--format", "{{.Names}}"); err != nil {
		fmt.Fprintf(w, "docker\tfailed\t%s\n", err)
		return false
	}
	fmt.Fprintf(w, "docker\tok\tserver=%s\n", version)
	return true
}

func doctorDeployInfrastructure(w io.Writer) bool {
	failed := false
	if err := commandRunFunc(3*time.Second, "id", "deploy"); err != nil {
		fmt.Fprintf(w, "deploy\tuser\tfailed\t%s\n", err)
		failed = true
	} else {
		fmt.Fprintln(w, "deploy\tuser\tok\tdeploy")
	}

	groups, err := commandOutputFunc(3*time.Second, "id", "-nG", "deploy")
	if err != nil {
		fmt.Fprintf(w, "deploy\tdocker_group\tfailed\t%s\n", err)
		failed = true
	} else if !hasWord(groups, "docker") {
		fmt.Fprintf(w, "deploy\tdocker_group\tfailed\tdeploy groups=%s\n", compactWhitespace(groups))
		failed = true
	} else {
		fmt.Fprintf(w, "deploy\tdocker_group\tok\tgroups=%s\n", compactWhitespace(groups))
	}

	keyPath := "/root/.ssh/id_ed25519"
	if stat, err := os.Stat(keyPath); err != nil {
		fmt.Fprintf(w, "deploy\tssh_key\tfailed\t%s\n", err)
		failed = true
	} else if stat.IsDir() {
		fmt.Fprintf(w, "deploy\tssh_key\tfailed\t%s is a directory\n", keyPath)
		failed = true
	} else {
		fmt.Fprintf(w, "deploy\tssh_key\tok\t%s\n", keyPath)
	}

	if err := commandRunFunc(5*time.Second, "ssh", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null", "-o", "ConnectTimeout=3", "deploy@127.0.0.1", "true"); err != nil {
		fmt.Fprintf(w, "deploy\tssh\tfailed\t%s\n", err)
		failed = true
	} else {
		fmt.Fprintln(w, "deploy\tssh\tok\tdeploy@127.0.0.1")
	}

	status, err := registryHealthStatus()
	if err != nil {
		fmt.Fprintf(w, "registry\tfailed\t%s\n", err)
		failed = true
	} else {
		fmt.Fprintf(w, "registry\tok\t%s\n", status)
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
		fmt.Fprintf(w, "disk\tfailed\t%s\t%s\n", path, err)
		return false
	}
	total := uint64(stat.Blocks) * uint64(stat.Bsize)
	available := uint64(stat.Bavail) * uint64(stat.Bsize)
	availablePercent := 0.0
	if total > 0 {
		availablePercent = float64(available) * 100 / float64(total)
	}
	if available < 1<<30 || availablePercent < 5 {
		fmt.Fprintf(w, "disk\tfailed\t%s\tavailable=%s\tavailable_percent=%.1f\n", path, formatBytesGB(available), availablePercent)
		return false
	}
	fmt.Fprintf(w, "disk\tok\t%s\tavailable=%s\tavailable_percent=%.1f\n", path, formatBytesGB(available), availablePercent)
	return true
}

func doctorCloudflare(w io.Writer, allApps []AppConfig, selectedApps []AppConfig) bool {
	state, err := loadCloudflareState()
	if err != nil {
		fmt.Fprintf(w, "cloudflare\tfailed\t%s\n", err)
		return false
	}
	if state.TunnelID == "" {
		if appsHaveHosts(selectedApps) {
			fmt.Fprintln(w, "cloudflare\tfailed\tconnect Cloudflare first with `singleserver cloudflare connect`")
			return false
		}
		fmt.Fprintln(w, "cloudflare\tskipped\tno tunnel configured")
		return true
	}

	failed := false
	fmt.Fprintf(w, "cloudflare\tstate\tok\tzone=%s\ttunnel=%s\thook=%s\n", valueOrDash(state.ZoneName), state.TunnelID, valueOrDash(state.HookHost))
	for label, path := range map[string]string{
		"credentials": state.CredentialsFile,
		"config":      state.ConfigFile,
	} {
		if path == "" {
			fmt.Fprintf(w, "cloudflare\t%s\tfailed\tmissing path\n", label)
			failed = true
			continue
		}
		if _, err := os.Stat(path); err != nil {
			fmt.Fprintf(w, "cloudflare\t%s\tfailed\t%s\n", label, err)
			failed = true
			continue
		}
		fmt.Fprintf(w, "cloudflare\t%s\tok\t%s\n", label, path)
	}

	if err := commandRunFunc(5*time.Second, "systemctl", "is-active", "--quiet", "cloudflared-singleserver.service"); err != nil {
		fmt.Fprintf(w, "cloudflare\tservice\tfailed\t%s\n", err)
		failed = true
	} else {
		fmt.Fprintln(w, "cloudflare\tservice\tok\tcloudflared-singleserver.service")
	}

	routes := map[string]string{}
	if state.ConfigFile != "" {
		config, err := readCloudflaredConfig(state.ConfigFile)
		if err != nil {
			fmt.Fprintf(w, "cloudflare\troutes\tfailed\t%s\n", err)
			failed = true
		} else {
			routes = cloudflaredRoutes(config)
			fmt.Fprintf(w, "cloudflare\troutes\tok\tcount=%d\n", len(routes))
		}
	}

	cloudflareClient, cloudflareClientOK := doctorCloudflareClient(w, state)
	if !cloudflareClientOK {
		failed = true
	}

	if state.HookHost != "" {
		if !doctorHostResolves(w, "cloudflare", "hook_dns", state.HookHost) {
			failed = true
		}
		if cloudflareClient != nil && !doctorCloudflareDNSRecord(w, "cloudflare", "hook_cloudflare_dns", state.HookHost, state, cloudflareClient) {
			failed = true
		}
		if service := routes[strings.ToLower(state.HookHost)]; service == "" {
			fmt.Fprintf(w, "cloudflare\thook_route\tfailed\t%s missing from %s\n", state.HookHost, state.ConfigFile)
			failed = true
		} else {
			fmt.Fprintf(w, "cloudflare\thook_route\tok\t%s -> %s\n", state.HookHost, service)
		}
	}

	expectedHosts := expectedCloudflaredHosts(state.HookHost, allApps)
	for _, host := range staleCloudflaredHosts(routes, expectedHosts) {
		fmt.Fprintf(w, "cloudflare\tstale_route\tfailed\t%s -> %s not in apps.yml\n", host, routes[host])
		failed = true
	}

	for _, app := range selectedApps {
		for _, host := range app.Hosts {
			if !doctorHostResolves(w, app.Name, "dns", host) {
				failed = true
			}
			if cloudflareClient != nil && !doctorCloudflareDNSRecord(w, app.Name, "cloudflare_dns", host, state, cloudflareClient) {
				failed = true
			}
			if service := routes[strings.ToLower(host)]; service == "" {
				fmt.Fprintf(w, "%s\ttunnel_route\tfailed\t%s missing from %s\n", app.Name, host, state.ConfigFile)
				failed = true
			} else {
				fmt.Fprintf(w, "%s\ttunnel_route\tok\t%s -> %s\n", app.Name, host, service)
			}
		}
	}

	return !failed
}

func doctorCloudflareClient(w io.Writer, state *CloudflareState) (*CloudflareClient, bool) {
	if state.ZoneID == "" || state.TunnelID == "" {
		return nil, true
	}
	token := cloudflareTokenFromEnvOrState(state)
	if token == "" {
		return nil, true
	}
	client, err := newCloudflareClient(token)
	if err != nil {
		fmt.Fprintf(w, "cloudflare\tdns_api\tfailed\t%s\n", err)
		return nil, false
	}
	return client, true
}

func doctorCloudflareDNSRecord(w io.Writer, scope string, check string, host string, state *CloudflareState, client *CloudflareClient) bool {
	target, err := verifyCloudflareDNSRecordFunc(host, state, client)
	if err != nil {
		fmt.Fprintf(w, "%s\t%s\tfailed\t%s\t%s\n", scope, check, host, err)
		return false
	}
	fmt.Fprintf(w, "%s\t%s\tok\t%s -> %s\n", scope, check, host, target)
	return true
}

func doctorGitHubSetup(w io.Writer, github *GitHubClient, appCount int) bool {
	secrets, err := github.LoadSecrets()
	if err != nil {
		status := "pending"
		if appCount > 0 {
			status = "failed"
		}
		fmt.Fprintf(w, "github\tsetup\t%s\trun `singleserver github connect`\t%s\n", status, err)
		return appCount == 0
	}
	if _, err := github.loadPrivateKey(); err != nil {
		status := "pending"
		if appCount > 0 {
			status = "failed"
		}
		fmt.Fprintf(w, "github\tsetup\t%s\tprivate key unavailable\t%s\n", status, err)
		return appCount == 0
	}
	fmt.Fprintf(w, "github\tsetup\tok\tapp_id=%d\tslug=%s\n", secrets.AppID, valueOrDash(secrets.Slug))
	return true
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
			fmt.Fprintf(w, "daemon\tok\t%s\n", status)
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
		fmt.Fprintf(w, "daemon\tfailed\t%s\n", lastStatus)
	} else {
		fmt.Fprintf(w, "daemon\tfailed\t%s\n", lastErr)
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
		fmt.Fprintf(w, "%s\t%s\tfailed\t%s\t%s\n", scope, check, host, err)
		return false
	}
	fmt.Fprintf(w, "%s\t%s\tok\t%s\n", scope, check, host)
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

func expectedCloudflaredHosts(hookHost string, apps []AppConfig) map[string]bool {
	hosts := map[string]bool{}
	if strings.TrimSpace(hookHost) != "" {
		hosts[strings.ToLower(strings.TrimSpace(hookHost))] = true
	}
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
