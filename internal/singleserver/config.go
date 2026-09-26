package singleserver

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var (
	repoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*$`)
)

type Config struct {
	Apps []AppConfig `yaml:"apps"`
}

type Tunnel string

const (
	TunnelPublic  Tunnel = "public"
	TunnelPrivate Tunnel = "private"
)

func (t Tunnel) IsPrivate() bool { return t == TunnelPrivate }

type AppConfig struct {
	Repo            string         `yaml:"repo"`
	Name            string         `yaml:"name"`
	Branch          string         `yaml:"branch"`
	RepoDir         string         `yaml:"path"`
	Healthcheck     string         `yaml:"healthcheck"`
	Hosts           []string       `yaml:"hosts"`
	Tunnel          Tunnel         `yaml:"tunnel,omitempty"`
	AppPort         int            `yaml:"app_port"`
	AppPortSet      bool           `yaml:"-"`
	HealthcheckPath string         `yaml:"healthcheck_path"`
	Runtime         string         `yaml:"runtime,omitempty"`
	InstallCommand  string         `yaml:"install,omitempty"`
	BuildCommand    string         `yaml:"build,omitempty"`
	StartCommand    string         `yaml:"start,omitempty"`
	StaticDir       string         `yaml:"static_dir,omitempty"`
	DeployTimeout   string         `yaml:"deploy_timeout,omitempty"`
	StopTimeout     string         `yaml:"stop_timeout,omitempty"`
	DrainTimeout    string         `yaml:"drain_timeout,omitempty"`
	Storage         *StorageConfig `yaml:"storage,omitempty"`
	Funnel          *FunnelConfig  `yaml:"funnel,omitempty"`
	SecretEnvKeys   []string       `yaml:"-"`
}

type StorageConfig struct {
	Path  string `yaml:"path,omitempty"`
	Mount string `yaml:"mount,omitempty"`
}

// Paths of a private app published to the internet through Tailscale Funnel, on their own node name.
type FunnelConfig struct {
	Host  string   `yaml:"host,omitempty"`
	Paths []string `yaml:"paths"`
}

func (a *AppConfig) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		a.Repo = strings.TrimSpace(value.Value)
	case yaml.MappingNode:
		type rawApp AppConfig
		var raw rawApp
		if err := value.Decode(&raw); err != nil {
			return err
		}
		*a = AppConfig(raw)
		a.AppPortSet = mappingHasKey(value, "app_port")
	default:
		return fmt.Errorf("app entry must be a repo string or map")
	}
	return a.Normalize()
}

func (a *AppConfig) Normalize() error {
	a.Repo = strings.TrimSpace(a.Repo)
	if !repoPattern.MatchString(a.Repo) {
		return fmt.Errorf("invalid repo: %q", a.Repo)
	}

	repoName := strings.Split(a.Repo, "/")[1]
	if strings.TrimSpace(a.Name) == "" {
		a.Name = repoName
	}
	a.Name = strings.ToLower(strings.TrimSpace(a.Name))
	if !namePattern.MatchString(a.Name) {
		return fmt.Errorf("invalid app name for %s: %q", a.Repo, a.Name)
	}

	a.Branch = strings.TrimSpace(a.Branch)
	a.RepoDir = strings.TrimSpace(a.RepoDir)
	if a.RepoDir == "" {
		a.RepoDir = filepath.Join(reposRoot(), a.Name)
	}
	a.Healthcheck = strings.TrimSpace(a.Healthcheck)
	a.Runtime = strings.ToLower(strings.TrimSpace(a.Runtime))
	a.InstallCommand = strings.TrimSpace(a.InstallCommand)
	a.BuildCommand = strings.TrimSpace(a.BuildCommand)
	a.StartCommand = strings.TrimSpace(a.StartCommand)
	a.StaticDir = strings.TrimSpace(a.StaticDir)
	a.DeployTimeout = strings.TrimSpace(a.DeployTimeout)
	if a.DeployTimeout != "" {
		parsed, err := time.ParseDuration(a.DeployTimeout)
		if err != nil {
			return fmt.Errorf("invalid deploy_timeout for %s: %q", a.Repo, a.DeployTimeout)
		}
		if parsed <= 0 {
			return fmt.Errorf("deploy_timeout for %s must be positive: %q", a.Repo, a.DeployTimeout)
		}
	}
	a.StopTimeout = strings.TrimSpace(a.StopTimeout)
	if a.StopTimeout != "" {
		parsed, err := strconv.Atoi(a.StopTimeout)
		if err != nil {
			return fmt.Errorf("invalid stop_timeout for %s: %q", a.Repo, a.StopTimeout)
		}
		if parsed <= 0 {
			return fmt.Errorf("stop_timeout for %s must be positive: %q", a.Repo, a.StopTimeout)
		}
	}
	a.DrainTimeout = strings.TrimSpace(a.DrainTimeout)
	if a.DrainTimeout != "" {
		parsed, err := strconv.Atoi(a.DrainTimeout)
		if err != nil {
			return fmt.Errorf("invalid drain_timeout for %s: %q", a.Repo, a.DrainTimeout)
		}
		if parsed <= 0 {
			return fmt.Errorf("drain_timeout for %s must be positive: %q", a.Repo, a.DrainTimeout)
		}
	}
	if a.AppPort == 0 {
		a.AppPort = 80
	}
	if a.AppPort < 1 || a.AppPort > 65535 {
		return fmt.Errorf("invalid app_port for %s: %d", a.Repo, a.AppPort)
	}

	hosts := make([]string, 0, len(a.Hosts))
	seenHosts := map[string]bool{}
	for _, host := range a.Hosts {
		host = strings.TrimSpace(host)
		if host == "" {
			continue
		}
		if strings.Contains(host, "://") || strings.Contains(host, "/") {
			return fmt.Errorf("invalid host for %s: %q", a.Repo, host)
		}
		key := strings.ToLower(host)
		if seenHosts[key] {
			continue
		}
		seenHosts[key] = true
		hosts = append(hosts, host)
	}
	a.Hosts = hosts

	a.Tunnel = Tunnel(strings.ToLower(strings.TrimSpace(string(a.Tunnel))))
	if a.Tunnel == "" {
		a.Tunnel = TunnelPublic
		for _, host := range a.Hosts {
			if isTailnetHost(host) {
				a.Tunnel = TunnelPrivate
				break
			}
		}
	}
	if a.Tunnel != TunnelPublic && a.Tunnel != TunnelPrivate {
		return fmt.Errorf("invalid tunnel for %s: %q (expected %s or %s)", a.Repo, a.Tunnel, TunnelPublic, TunnelPrivate)
	}
	if a.IsPrivate() {
		if len(a.Hosts) > 1 {
			return fmt.Errorf("private tailscale tunnel for %s supports at most one domain: %v", a.Repo, a.Hosts)
		}
		for _, host := range a.Hosts {
			if strings.Contains(host, ".") && !isTailnetHost(host) {
				return fmt.Errorf("private tailscale tunnel for %s requires a .ts.net domain or a bare name, got %q", a.Repo, host)
			}
		}
	}

	a.HealthcheckPath = strings.TrimSpace(a.HealthcheckPath)
	if a.HealthcheckPath == "" {
		a.HealthcheckPath = defaultHealthcheckPath(*a)
	}
	if !strings.HasPrefix(a.HealthcheckPath, "/") {
		a.HealthcheckPath = "/" + a.HealthcheckPath
	}
	if err := a.normalizeGeneratedDockerfileConfig(); err != nil {
		return err
	}
	if a.Storage != nil {
		a.Storage.Path = strings.TrimSpace(a.Storage.Path)
		if a.Storage.Path == "" {
			a.Storage.Path = filepath.Join(storageRoot(), a.Name)
		}
		if !strings.HasPrefix(a.Storage.Path, "/") {
			return fmt.Errorf("storage path for %s must be absolute: %q", a.Repo, a.Storage.Path)
		}
		a.Storage.Mount = strings.TrimSpace(a.Storage.Mount)
		if a.Storage.Mount == "" {
			a.Storage.Mount = "/storage"
		}
		if !strings.HasPrefix(a.Storage.Mount, "/") {
			return fmt.Errorf("storage mount for %s must be absolute: %q", a.Repo, a.Storage.Mount)
		}
	}
	return a.normalizeFunnel()
}

func (a *AppConfig) normalizeFunnel() error {
	if a.Funnel == nil {
		return nil
	}
	if !a.IsPrivate() {
		return fmt.Errorf("funnel for %s requires tunnel: private; public apps are already on the internet through Cloudflare", a.Repo)
	}
	a.Funnel.Host = strings.ToLower(strings.TrimSpace(a.Funnel.Host))
	if a.Funnel.Host == "" {
		a.Funnel.Host = a.Name + "-public"
	}
	if strings.Contains(a.Funnel.Host, "://") || strings.Contains(a.Funnel.Host, "/") {
		return fmt.Errorf("invalid funnel host for %s: %q", a.Repo, a.Funnel.Host)
	}
	if strings.Contains(a.Funnel.Host, ".") && !isTailnetHost(a.Funnel.Host) {
		return fmt.Errorf("funnel host for %s must be a .ts.net name or a bare label, got %q", a.Repo, a.Funnel.Host)
	}
	if !namePattern.MatchString(a.FunnelLabel()) {
		return fmt.Errorf("invalid funnel host for %s: %q", a.Repo, a.Funnel.Host)
	}
	if a.FunnelLabel() == tailscaleServiceName(*a) {
		return fmt.Errorf("funnel host for %s must differ from the app's own name %s; the funnel is a separate node on the tailnet", a.Repo, a.FunnelLabel())
	}

	paths := make([]string, 0, len(a.Funnel.Paths))
	seen := map[string]bool{}
	for _, p := range a.Funnel.Paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if p != "/" {
			p = strings.TrimRight(p, "/")
		}
		if !strings.HasPrefix(p, "/") || strings.ContainsAny(p, "?# \t") || path.Clean(p) != p {
			return fmt.Errorf("invalid funnel path for %s: %q (a clean absolute path with no query or spaces)", a.Repo, p)
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		paths = append(paths, p)
	}
	if len(paths) == 0 {
		return fmt.Errorf("funnel for %s needs at least one path", a.Repo)
	}
	a.Funnel.Paths = paths
	return nil
}

const defaultDeployTimeout = 10 * time.Minute

func (a AppConfig) DeployTimeoutDuration() time.Duration {
	if parsed, err := time.ParseDuration(a.DeployTimeout); err == nil && parsed > 0 {
		return parsed
	}
	return defaultDeployTimeout
}

const defaultStopTimeout = 1

func (a AppConfig) StopTimeoutSeconds() int {
	if parsed, err := strconv.Atoi(a.StopTimeout); err == nil && parsed > 0 {
		return parsed
	}
	return defaultStopTimeout
}

const defaultDrainTimeout = 1

func (a AppConfig) DrainTimeoutSeconds() int {
	if parsed, err := strconv.Atoi(a.DrainTimeout); err == nil && parsed > 0 {
		return parsed
	}
	return defaultDrainTimeout
}

func reposRoot() string {
	return envDefault("SINGLESERVER_REPOS_ROOT", "/srv/repos")
}

func storageRoot() string {
	return envDefault("SINGLESERVER_STORAGE_ROOT", "/srv/storage")
}

func (a AppConfig) IsPrivate() bool { return a.Tunnel.IsPrivate() }

// QualifiedHost expands a private app's bare label into its MagicDNS name.
func (a AppConfig) QualifiedHost(host string) string {
	if !a.IsPrivate() || strings.Contains(host, ".") {
		return host
	}
	domain := tailnetDomain()
	if domain == "" {
		return host
	}
	return host + "." + domain
}

func (a AppConfig) QualifiedHosts() []string {
	hosts := make([]string, len(a.Hosts))
	for i, host := range a.Hosts {
		hosts[i] = a.QualifiedHost(host)
	}
	return hosts
}

func (a AppConfig) HasFunnel() bool { return a.Funnel != nil && len(a.Funnel.Paths) > 0 }

// FunnelLabel is the funnel node's machine name, the first label of its host.
func (a AppConfig) FunnelLabel() string {
	if a.Funnel == nil {
		return ""
	}
	label, _, _ := strings.Cut(a.Funnel.Host, ".")
	return label
}

func (a AppConfig) FunnelHost() string {
	if a.Funnel == nil {
		return ""
	}
	return a.QualifiedHost(a.Funnel.Host)
}

func (a AppConfig) FunnelURL() string {
	if !a.HasFunnel() {
		return ""
	}
	return "https://" + a.FunnelHost() + a.Funnel.Paths[0]
}

// ProxyHosts is every hostname Kamal's proxy must route to this app.
func (a AppConfig) ProxyHosts() []string {
	hosts := a.QualifiedHosts()
	if a.HasFunnel() {
		hosts = append(hosts, a.FunnelHost())
	}
	return hosts
}

func defaultHealthcheckPath(app AppConfig) string {
	if app.Runtime == "static" || strings.TrimSpace(app.StaticDir) != "" {
		return "/up"
	}
	return "/"
}

func (a *AppConfig) normalizeGeneratedDockerfileConfig() error {
	if a.Runtime == "" {
		if a.InstallCommand != "" {
			return fmt.Errorf("install requires runtime for %s", a.Repo)
		}
		if a.BuildCommand != "" {
			return fmt.Errorf("build requires runtime for %s", a.Repo)
		}
		if a.StartCommand != "" {
			return fmt.Errorf("start requires runtime for %s", a.Repo)
		}
		if a.StaticDir != "" {
			return fmt.Errorf("static_dir requires runtime for %s", a.Repo)
		}
		return nil
	}

	switch a.Runtime {
	case "static", "node", "bun":
	default:
		return fmt.Errorf("invalid runtime for %s: %q", a.Repo, a.Runtime)
	}

	if err := validateGeneratedCommand("install", a.InstallCommand, a.Repo); err != nil {
		return err
	}
	if err := validateGeneratedCommand("build", a.BuildCommand, a.Repo); err != nil {
		return err
	}
	if err := validateGeneratedCommand("start", a.StartCommand, a.Repo); err != nil {
		return err
	}

	if a.Runtime == "static" && a.StaticDir == "" {
		a.StaticDir = "."
	}
	if a.StaticDir != "" {
		normalized, err := normalizeStaticDir(a.StaticDir)
		if err != nil {
			return fmt.Errorf("invalid static_dir for %s: %w", a.Repo, err)
		}
		a.StaticDir = normalized
	}

	staticOutput := a.Runtime == "static" || a.StaticDir != ""
	if staticOutput {
		if a.StartCommand != "" {
			return fmt.Errorf("start is not used when %s builds static files for %s", a.Runtime, a.Repo)
		}
		if a.AppPort != 80 {
			return fmt.Errorf("static output for %s must use app_port 80", a.Repo)
		}
	}

	if a.Runtime == "static" {
		if a.InstallCommand != "" {
			return fmt.Errorf("install is not used with runtime static for %s", a.Repo)
		}
		if a.BuildCommand != "" {
			return fmt.Errorf("build is not used with runtime static for %s", a.Repo)
		}
		return nil
	}

	if !staticOutput {
		if a.StartCommand == "" {
			return fmt.Errorf("runtime %s requires start for %s", a.Runtime, a.Repo)
		}
		if !a.AppPortSet {
			return fmt.Errorf("runtime %s requires app_port for %s", a.Runtime, a.Repo)
		}
	}
	return nil
}

func validateGeneratedCommand(name, command, repo string) error {
	if command == "" {
		return nil
	}
	if strings.ContainsAny(command, "\x00\r\n") {
		return fmt.Errorf("%s for %s must be a single shell command", name, repo)
	}
	return nil
}

func normalizeStaticDir(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("cannot be empty")
	}
	if strings.HasPrefix(value, "/") {
		return "", fmt.Errorf("must be relative: %q", value)
	}
	cleaned := path.Clean(value)
	if cleaned == "." {
		return ".", nil
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.Contains(cleaned, "/../") {
		return "", fmt.Errorf("must stay inside the repository: %q", value)
	}
	return cleaned, nil
}

func mappingHasKey(node *yaml.Node, key string) bool {
	if node == nil || node.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return true
		}
	}
	return false
}

func LoadConfig(path string) (*Config, error) {
	if path == "" {
		path = "/etc/singleserver/apps.yml"
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var config Config
	if err := yaml.Unmarshal(body, &config); err != nil {
		return nil, err
	}
	if err := config.Normalize(); err != nil {
		return nil, err
	}
	return &config, nil
}

func (c *Config) Normalize() error {
	seenRepos := map[string]bool{}
	seenNames := map[string]string{}
	seenHosts := map[string]string{}
	seenServices := map[string]string{}
	for i := range c.Apps {
		if err := c.Apps[i].Normalize(); err != nil {
			return err
		}
		repoKey := strings.ToLower(c.Apps[i].Repo)
		if seenRepos[repoKey] {
			return fmt.Errorf("duplicate repo in config: %s", c.Apps[i].Repo)
		}
		seenRepos[repoKey] = true

		nameKey := strings.ToLower(c.Apps[i].Name)
		if existingRepo := seenNames[nameKey]; existingRepo != "" {
			return fmt.Errorf("duplicate app name in config: %s is used by %s and %s", c.Apps[i].Name, existingRepo, c.Apps[i].Repo)
		}
		seenNames[nameKey] = c.Apps[i].Repo

		for _, host := range c.Apps[i].Hosts {
			hostKey := strings.ToLower(host)
			if existingRepo := seenHosts[hostKey]; existingRepo != "" {
				return fmt.Errorf("duplicate host in config: %s is used by %s and %s", host, existingRepo, c.Apps[i].Repo)
			}
			seenHosts[hostKey] = c.Apps[i].Repo
		}

		// Distinct hosts can still collapse to one service name, its first label.
		if c.Apps[i].IsPrivate() {
			serviceKey := tailscaleServiceName(c.Apps[i])
			if existingRepo := seenServices[serviceKey]; existingRepo != "" {
				return fmt.Errorf("duplicate tailscale service in config: %s and %s both resolve to svc:%s; give them distinct first domain labels", existingRepo, c.Apps[i].Repo, serviceKey)
			}
			seenServices[serviceKey] = c.Apps[i].Repo
		}
	}
	// Funnel nodes share the MagicDNS namespace with services, so check them after every service is known.
	for i := range c.Apps {
		if !c.Apps[i].HasFunnel() {
			continue
		}
		label := c.Apps[i].FunnelLabel()
		if existingRepo := seenServices[label]; existingRepo != "" {
			return fmt.Errorf("duplicate tailscale name in config: the funnel host of %s collides with %s on %s; pick another funnel host", c.Apps[i].Repo, existingRepo, label)
		}
		seenServices[label] = c.Apps[i].Repo
	}
	return nil
}

func (c *Config) AppByRepo(repo string) (*AppConfig, bool) {
	for i := range c.Apps {
		if strings.EqualFold(c.Apps[i].Repo, repo) {
			return &c.Apps[i], true
		}
	}
	return nil, false
}

func (c *Config) AppByName(name string) (*AppConfig, bool) {
	for i := range c.Apps {
		if strings.EqualFold(c.Apps[i].Name, name) {
			return &c.Apps[i], true
		}
	}
	return nil, false
}

func (c *Config) AppByNameOrRepo(value string) (*AppConfig, bool) {
	for i := range c.Apps {
		if appMatches(c.Apps[i], value) {
			return &c.Apps[i], true
		}
	}
	return nil, false
}

func (c *Config) AppForPush(payload *PushPayload) (*AppConfig, string, string) {
	if payload == nil || payload.Repository.FullName == "" {
		return nil, "", "missing repository"
	}
	branch := branchFromRef(payload.Ref)
	if branch == "" {
		return nil, "", "unsupported push ref"
	}
	for i := range c.Apps {
		app := &c.Apps[i]
		if !strings.EqualFold(app.Repo, payload.Repository.FullName) {
			continue
		}
		targetBranch := strings.TrimSpace(app.Branch)
		if targetBranch == "" {
			targetBranch = strings.TrimSpace(payload.Repository.DefaultBranch)
		}
		if targetBranch == "" {
			return nil, branch, app.Repo + " default branch is missing"
		}
		if branch != targetBranch {
			return nil, branch, fmt.Sprintf("%s:%s does not match %s", app.Repo, branch, targetBranch)
		}
		return app, branch, ""
	}
	return nil, branch, payload.Repository.FullName + " is not configured"
}

func branchFromRef(ref string) string {
	const prefix = "refs/heads/"
	if !strings.HasPrefix(ref, prefix) {
		return ""
	}
	return strings.TrimPrefix(ref, prefix)
}

func requireEnv(name string) (string, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return "", errors.New(name + " is required")
	}
	return value, nil
}
