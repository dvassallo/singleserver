package singleserver

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

func cliEnv(args []string, w io.Writer) error {
	mode, args, err := commandModeFromArgs(args, noFlagValues)
	if err != nil {
		return err
	}
	prompting := cliCanPrompt(mode)
	p := interactivePrompter(w)
	if len(args) == 0 && prompting {
		action, err := p.askChoice("Env action", []string{"set", "list", "unset"})
		if err != nil {
			return err
		}
		args = append(args, action)
	}
	if len(args) < 2 && prompting {
		appName, err := promptConfiguredAppName(p, "App")
		if err != nil {
			return err
		}
		args = append(args, appName)
	}
	if len(args) < 2 {
		return errors.New("usage: singleserver env <set|list|unset> <app> [KEY=value|KEY]")
	}
	command := args[0]
	appName := args[1]
	app, err := configuredApp(appName)
	if err != nil {
		return err
	}

	switch command {
	case "set":
		if len(args) < 3 && prompting {
			key, err := p.askRequired("Env key")
			if err != nil {
				return err
			}
			value, err := p.askOptional("Env value")
			if err != nil {
				return err
			}
			args = append(args, key+"="+value)
		}
		if len(args) != 3 {
			return errors.New("usage: singleserver env set <app> KEY=value")
		}
		key, value, err := parseKeyValue(args[2])
		if err != nil {
			return err
		}
		values, err := loadAppEnv(app.Name)
		if err != nil {
			return err
		}
		values[key] = value
		if err := writeAppEnv(app.Name, values); err != nil {
			return err
		}
		writeCheck(w, app.Name, "env", "ok", key, "set")
		writeCheck(w, app.Name, "next", "pending", "deploy with `singleserver deploy "+app.Repo+"`")
	case "list":
		if len(args) != 2 {
			return errors.New("usage: singleserver env list <app>")
		}
		values, err := loadAppEnv(app.Name)
		if err != nil {
			return err
		}
		for _, key := range sortedEnvKeys(values) {
			fmt.Fprintf(w, "%s=%s\n", key, values[key])
		}
	case "unset":
		if len(args) < 3 && prompting {
			key, err := p.askRequired("Env key")
			if err != nil {
				return err
			}
			args = append(args, key)
		}
		if len(args) != 3 {
			return errors.New("usage: singleserver env unset <app> KEY")
		}
		key := strings.TrimSpace(args[2])
		if !envKeyPattern.MatchString(key) {
			return fmt.Errorf("invalid env key: %q", key)
		}
		values, err := loadAppEnv(app.Name)
		if err != nil {
			return err
		}
		delete(values, key)
		if err := writeAppEnv(app.Name, values); err != nil {
			return err
		}
		writeCheck(w, app.Name, "env", "ok", key, "unset")
		writeCheck(w, app.Name, "next", "pending", "deploy with `singleserver deploy "+app.Repo+"`")
	default:
		return fmt.Errorf("unknown env command %q", command)
	}
	return nil
}
