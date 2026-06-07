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
	Repo        string `yaml:"repo"`
	Name        string `yaml:"name"`
	Branch      string `yaml:"branch"`
	RepoDir     string `yaml:"path"`
	Healthcheck string `yaml:"healthcheck"`
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
	seen := map[string]bool{}
	for i := range config.Apps {
		if err := config.Apps[i].Normalize(); err != nil {
			return nil, err
		}
		key := strings.ToLower(config.Apps[i].Repo)
		if seen[key] {
			return nil, fmt.Errorf("duplicate repo in config: %s", config.Apps[i].Repo)
		}
		seen[key] = true
	}
	return &config, nil
}

func (c *Config) AppByRepo(repo string) (*AppConfig, bool) {
	for i := range c.Apps {
		if strings.EqualFold(c.Apps[i].Repo, repo) {
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
		targetBranch := app.Branch
		if targetBranch == "" {
			targetBranch = payload.Repository.DefaultBranch
		}
		if targetBranch != "" && branch != targetBranch {
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
