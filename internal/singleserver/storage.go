package singleserver

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func cliStorage(args []string, w io.Writer, logger *log.Logger) error {
	mode, args, err := commandModeFromArgs(args, noFlagValues)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return errors.New("usage: singleserver storage <enable|disable> <app> [options]")
	}
	return withCLIMode(mode, func() error {
		switch args[0] {
		case "enable":
			return cliStorageEnable(args[1:], w, logger)
		case "disable":
			return cliStorageDisable(args[1:], w, logger)
		default:
			return fmt.Errorf("unknown storage command %q", args[0])
		}
	})
}

func cliStorageEnable(args []string, w io.Writer, logger *log.Logger) error {
	mode, args, err := commandModeFromArgs(args, storageFlagTakesValue)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("storage enable", flag.ContinueOnError)
	fs.SetOutput(w)
	mount := fs.String("mount", "/storage", "container mount path")
	path := fs.String("path", "", "host storage path")
	noDeploy := fs.Bool("no-deploy", false, "update config without deploying")
	mountSet := false
	pathSet := false
	if err := fs.Parse(normalizeFlagArgs(args, storageFlagTakesValue)); err != nil {
		return err
	}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "mount":
			mountSet = true
		case "path":
			pathSet = true
		}
	})
	prompting := cliCanPrompt(mode)
	appName := ""
	if fs.NArg() == 1 {
		appName = fs.Arg(0)
	} else if fs.NArg() == 0 && prompting {
		appName, err = promptConfiguredAppName(interactivePrompter(w), "App")
		if err != nil {
			return err
		}
	} else {
		return errors.New("usage: singleserver storage enable <app> [--mount /storage] [--path /srv/storage/app] [--no-deploy]")
	}

	configPath := envDefault("SINGLESERVER_CONFIG", "/etc/singleserver/apps.yml")
	config, err := LoadConfig(configPath)
	if err != nil {
		return err
	}
	appIndex, err := config.AppIndex(appName)
	if err != nil {
		return err
	}

	if prompting {
		p := interactivePrompter(w)
		if !pathSet {
			value, err := p.askDefault("Host storage path", filepath.Join(storageRoot(), config.Apps[appIndex].Name))
			if err != nil {
				return err
			}
			*path = value
		}
		if !mountSet {
			value, err := p.askDefault("Container mount path", *mount)
			if err != nil {
				return err
			}
			*mount = value
		}
		if !*noDeploy {
			deploy, err := p.askYesNo("Deploy now?", true)
			if err != nil {
				return err
			}
			*noDeploy = !deploy
		}
	}

	app := &config.Apps[appIndex]
	app.Storage = &StorageConfig{Path: strings.TrimSpace(*path), Mount: strings.TrimSpace(*mount)}
	if err := app.Normalize(); err != nil {
		return err
	}
	storagePath := app.Storage.Path
	storageMount := app.Storage.Mount
	createdStorage := false
	if _, err := os.Stat(storagePath); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		createdStorage = true
	}
	if err := os.MkdirAll(storagePath, 0700); err != nil {
		return err
	}
	if err := chownStorage(storagePath); err != nil {
		if createdStorage {
			_ = os.Remove(storagePath)
		}
		return err
	}
	if err := writeConfigFunc(configPath, config); err != nil {
		if createdStorage {
			_ = os.Remove(storagePath)
		}
		return err
	}
	writeCheck(w, app.Name, "storage", "ok", storagePath, "mount="+storageMount)

	app, err = configuredApp(appName)
	if err != nil {
		return err
	}
	if *noDeploy {
		writeCheck(w, app.Name, "next", "pending", "deploy with `singleserver deploy "+app.Name+"`")
		return nil
	}
	writeCheck(w, app.Name, "deploy", "start", "applying storage change")
	if err := cliDeploy([]string{app.Name}, w, logger); err != nil {
		return err
	}
	return cliDoctor([]string{app.Name}, w)
}

func cliStorageDisable(args []string, w io.Writer, logger *log.Logger) error {
	mode, args, err := commandModeFromArgs(args, noFlagValues)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("storage disable", flag.ContinueOnError)
	fs.SetOutput(w)
	deleteData := fs.Bool("delete", false, "delete the storage directory")
	noDeploy := fs.Bool("no-deploy", false, "update config without deploying")
	deleteSet := false
	if err := fs.Parse(normalizeFlagArgs(args, noFlagValues)); err != nil {
		return err
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "delete" {
			deleteSet = true
		}
	})
	prompting := cliCanPrompt(mode)
	p := interactivePrompter(w)
	appName := ""
	if fs.NArg() == 1 {
		appName = fs.Arg(0)
	} else if fs.NArg() == 0 && prompting {
		appName, err = promptConfiguredAppName(p, "App")
		if err != nil {
			return err
		}
	} else {
		return errors.New("usage: singleserver storage disable <app> [--delete] [--no-deploy]")
	}

	configPath := envDefault("SINGLESERVER_CONFIG", "/etc/singleserver/apps.yml")
	config, err := LoadConfig(configPath)
	if err != nil {
		return err
	}
	appIndex, err := config.AppIndex(appName)
	if err != nil {
		return err
	}
	app := &config.Apps[appIndex]
	if app.Storage == nil {
		return fmt.Errorf("%s has no storage configured", app.Name)
	}
	if err := app.Normalize(); err != nil {
		return err
	}
	storagePath := app.Storage.Path

	if prompting {
		if !deleteSet {
			choice, err := p.askYesNo("Delete the storage directory "+storagePath+"?", false)
			if err != nil {
				return err
			}
			*deleteData = choice
		}
		if !*noDeploy {
			deploy, err := p.askYesNo("Deploy now?", true)
			if err != nil {
				return err
			}
			*noDeploy = !deploy
		}
	}

	app.Storage = nil
	if err := writeConfigFunc(configPath, config); err != nil {
		return err
	}
	writeCheck(w, app.Name, "config", "ok", configPath, "storage disabled")

	app, err = configuredApp(appName)
	if err != nil {
		return err
	}
	if *noDeploy {
		writeCheck(w, app.Name, "next", "pending", "deploy with `singleserver deploy "+app.Name+"`")
	} else {
		writeCheck(w, app.Name, "deploy", "start", "applying storage change")
		if err := cliDeploy([]string{app.Name}, w, logger); err != nil {
			return err
		}
	}

	if *deleteData {
		if err := os.RemoveAll(storagePath); err != nil {
			return err
		}
		writeCheck(w, app.Name, "storage", "ok", storagePath, "deleted")
	} else {
		writeCheck(w, app.Name, "storage", "kept", storagePath)
	}

	if *noDeploy {
		return nil
	}
	return cliDoctor([]string{app.Name}, w)
}

func requireStorage(app *AppConfig) (*StorageConfig, error) {
	if app.Storage == nil {
		return nil, fmt.Errorf("%s has no storage configured", app.Name)
	}
	if err := app.Normalize(); err != nil {
		return nil, err
	}
	return app.Storage, nil
}

func chownStorage(storagePath string) error {
	if err := commandRunFunc(3*time.Second, "chown", "-R", "deploy:docker", storagePath); err != nil {
		return fmt.Errorf("chown %s to deploy:docker: %w", storagePath, err)
	}
	return nil
}

func storageFlagTakesValue(arg string) bool {
	name := strings.TrimLeft(arg, "-")
	if before, _, ok := strings.Cut(name, "="); ok {
		name = before
	}
	return name == "mount" || name == "path"
}
