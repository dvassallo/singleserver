package singleserver

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

var (
	repoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*$`)
)

type Config struct {
	Apps []AppConfig `yaml:"apps"`
}

type AppConfig struct {
	Repo            string         `yaml:"repo"`
	Name            string         `yaml:"name"`
	Branch          string         `yaml:"branch"`
	RepoDir         string         `yaml:"path"`
	Healthcheck     string         `yaml:"healthcheck"`
	Hosts           []string       `yaml:"hosts"`
	AppPort         int            `yaml:"app_port"`
	HealthcheckPath string         `yaml:"healthcheck_path"`
	Storage         *StorageConfig `yaml:"storage,omitempty"`
	SecretEnvKeys   []string       `yaml:"-"`
}

type StorageConfig struct {
	Path  string `yaml:"path,omitempty"`
	Mount string `yaml:"mount,omitempty"`
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
		a.RepoDir = "/srv/repos/" + a.Name
	}
	a.Healthcheck = strings.TrimSpace(a.Healthcheck)
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

	a.HealthcheckPath = strings.TrimSpace(a.HealthcheckPath)
	if a.HealthcheckPath == "" {
		a.HealthcheckPath = "/up"
	}
	if !strings.HasPrefix(a.HealthcheckPath, "/") {
		a.HealthcheckPath = "/" + a.HealthcheckPath
	}
	if a.Storage != nil {
		a.Storage.Path = strings.TrimSpace(a.Storage.Path)
		if a.Storage.Path == "" {
			a.Storage.Path = "/srv/storage/" + a.Name
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
	return nil
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
