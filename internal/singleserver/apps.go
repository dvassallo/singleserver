package singleserver

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

func configuredApp(appName string) (*AppConfig, error) {
	config, err := LoadConfig(envDefault("SINGLESERVER_CONFIG", "/etc/singleserver/apps.yml"))
	if err != nil {
		return nil, err
	}
	index, err := config.AppIndex(appName)
	if err != nil {
		return nil, err
	}
	return &config.Apps[index], nil
}

func promptConfiguredAppName(p addPrompter, label string) (string, error) {
	config, err := LoadConfig(envDefault("SINGLESERVER_CONFIG", "/etc/singleserver/apps.yml"))
	if err != nil {
		return "", err
	}
	if len(config.Apps) == 0 {
		return "", errors.New("no apps configured; add your first app with `singleserver add https://github.com/owner/repo`")
	}
	if len(config.Apps) == 1 {
		return p.askDefault(label, config.Apps[0].Name)
	}
	fmt.Fprintln(p.w, "Configured apps:")
	for i, app := range config.Apps {
		fmt.Fprintf(p.w, "  %d. %s (%s)\n", i+1, app.Name, app.Repo)
	}
	for {
		value, err := p.askRequired(label)
		if err != nil {
			return "", err
		}
		if n, parseErr := strconv.Atoi(value); parseErr == nil && n >= 1 && n <= len(config.Apps) {
			return config.Apps[n-1].Name, nil
		}
		if index, err := config.AppIndex(value); err == nil {
			return config.Apps[index].Name, nil
		}
		fmt.Fprintln(p.w, "Enter an app name, repo, or number from the list.")
	}
}

func updateConfiguredApp(appName string, mutate func(app *AppConfig) error) error {
	configPath := envDefault("SINGLESERVER_CONFIG", "/etc/singleserver/apps.yml")
	config, err := LoadConfig(configPath)
	if err != nil {
		return err
	}
	index, err := config.AppIndex(appName)
	if err != nil {
		return err
	}
	if err := mutate(&config.Apps[index]); err != nil {
		return err
	}
	if err := config.Apps[index].Normalize(); err != nil {
		return err
	}
	return writeConfigFunc(configPath, config)
}

func appMatches(app AppConfig, value string) bool {
	return strings.EqualFold(app.Name, value) || strings.EqualFold(app.Repo, value)
}

// appsMatching returns every app whose name or repo equals value.
func appsMatching(apps []AppConfig, value string) []AppConfig {
	var matches []AppConfig
	for _, app := range apps {
		if appMatches(app, value) {
			matches = append(matches, app)
		}
	}
	return matches
}
