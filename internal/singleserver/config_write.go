package singleserver

import (
	"bytes"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

func loadConfigAllowMissing(path string) (*Config, error) {
	config, err := LoadConfig(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{}, nil
		}
		return nil, err
	}
	return config, nil
}

var writeConfigFunc = writeConfig

func writeConfig(path string, config *Config) error {
	if err := config.Normalize(); err != nil {
		return err
	}

	var doc yaml.Node
	doc.Kind = yaml.DocumentNode
	root := &yaml.Node{Kind: yaml.MappingNode}
	doc.Content = []*yaml.Node{root}
	apps := &yaml.Node{Kind: yaml.SequenceNode}
	root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "apps"}, apps)
	for _, app := range config.Apps {
		apps.Content = append(apps.Content, appConfigEntry(app).yamlNode())
	}

	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(&doc); err != nil {
		return err
	}
	if err := encoder.Close(); err != nil {
		return err
	}
	return writeFileAtomic(path, buf.Bytes())
}

func appConfigEntry(app AppConfig) addAppEntry {
	entry := addAppEntry{
		repo:            app.Repo,
		hosts:           app.Hosts,
		healthcheck:     app.Healthcheck,
		healthcheckPath: "",
		appPort:         app.AppPort,
		appPortSet:      app.AppPort != 0 && app.AppPort != 80,
		storage:         app.Storage,
	}
	repoName := ""
	if parts := strings.SplitN(app.Repo, "/", 2); len(parts) == 2 {
		repoName = parts[1]
	}
	if app.Name != "" && app.Name != repoName {
		entry.name = app.Name
	}
	if app.Branch != "" {
		entry.branch = app.Branch
	}
	if app.RepoDir != "" && app.RepoDir != "/srv/repos/"+app.Name {
		entry.repoDir = app.RepoDir
	}
	if app.HealthcheckPath != "" && app.HealthcheckPath != "/up" {
		entry.healthcheckPath = app.HealthcheckPath
	}
	return entry
}
