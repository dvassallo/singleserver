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
	if app, ok := config.AppByNameOrRepo(appName); ok {
		return app, nil
	}
	return nil, fmt.Errorf("%s is not configured", appName)
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
		for _, app := range config.Apps {
			if appMatches(app, value) {
				return app.Name, nil
			}
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
	for i := range config.Apps {
		if !appMatches(config.Apps[i], appName) {
			continue
		}
		if err := mutate(&config.Apps[i]); err != nil {
			return err
		}
		if err := config.Apps[i].Normalize(); err != nil {
			return err
		}
		return writeConfigFunc(configPath, config)
	}
	return fmt.Errorf("%s is not configured", appName)
}

func appMatches(app AppConfig, value string) bool {
	return strings.EqualFold(app.Name, value) || strings.EqualFold(app.Repo, value)
}
