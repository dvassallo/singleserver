package singleserver

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

func cliRemove(args []string, w io.Writer) error {
	mode, args, err := commandModeFromArgs(args, noFlagValues)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("remove", flag.ContinueOnError)
	fs.SetOutput(w)
	deleteStorage := fs.Bool("delete-storage", false, "delete persistent storage")
	deleteRepo := fs.Bool("delete-repo", false, "delete repository checkout")
	deleteStorageSet := false
	deleteRepoSet := false
	if err := fs.Parse(normalizeFlagArgs(args, noFlagValues)); err != nil {
		return err
	}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "delete-storage":
			deleteStorageSet = true
		case "delete-repo":
			deleteRepoSet = true
		}
	})
	prompting := cliCanPrompt(mode)
	p := interactivePrompter(w)
	appName := ""
	if fs.NArg() == 1 {
		appName = fs.Arg(0)
	} else if fs.NArg() == 0 && prompting {
		appName, err = promptConfiguredAppName(p, "App to remove")
		if err != nil {
			return err
		}
	} else {
		return errors.New("usage: singleserver remove <app> [--delete-storage] [--delete-repo]")
	}
	configPath := envDefault("SINGLESERVER_CONFIG", "/etc/singleserver/apps.yml")
	config, err := LoadConfig(configPath)
	if err != nil {
		return err
	}
	index := -1
	var app AppConfig
	for i := range config.Apps {
		if appMatches(config.Apps[i], appName) {
			index = i
			app = config.Apps[i]
			break
		}
	}
	if index < 0 {
		return fmt.Errorf("%s is not configured", appName)
	}

	if prompting {
		fmt.Fprintf(w, "Remove %s (%s).\n", app.Name, app.Repo)
		if app.Storage != nil && !deleteStorageSet {
			choice, err := p.askYesNo("Delete persistent storage?", false)
			if err != nil {
				return err
			}
			*deleteStorage = choice
		}
		if !deleteRepoSet {
			choice, err := p.askYesNo("Delete repository checkout?", false)
			if err != nil {
				return err
			}
			*deleteRepo = choice
		}
		proceed, err := p.askYesNo("Continue?", false)
		if err != nil {
			return err
		}
		if !proceed {
			writeCheck(w, app.Name, "remove", "canceled", "-")
			return nil
		}
	}

	removedHosts := []string{}
	for _, host := range app.Hosts {
		if err := syncCloudflareAppDomainFunc(host, false, w); err != nil {
			for _, removedHost := range removedHosts {
				_ = syncCloudflareAppDomainFunc(removedHost, true, io.Discard)
			}
			return err
		}
		removedHosts = append(removedHosts, host)
	}

	config.Apps = append(config.Apps[:index], config.Apps[index+1:]...)
	if err := writeConfig(configPath, config); err != nil {
		for _, removedHost := range removedHosts {
			_ = syncCloudflareAppDomainFunc(removedHost, true, io.Discard)
		}
		return err
	}
	writeCheck(w, app.Name, "config", "ok", configPath, "removed")
	if err := stopAppContainersFunc(app.Name); err != nil {
		writeCheck(w, app.Name, "containers", "failed", err.Error())
		return err
	} else {
		writeCheck(w, app.Name, "containers", "ok", "stopped matching containers")
	}
	if *deleteStorage && app.Storage != nil {
		if err := os.RemoveAll(app.Storage.Path); err != nil {
			return err
		}
		writeCheck(w, app.Name, "storage", "ok", app.Storage.Path, "deleted")
	} else if app.Storage != nil {
		writeCheck(w, app.Name, "storage", "kept", app.Storage.Path)
	}
	if *deleteRepo {
		if err := os.RemoveAll(app.RepoDir); err != nil {
			return err
		}
		writeCheck(w, app.Name, "repo", "ok", app.RepoDir, "deleted")
	} else {
		writeCheck(w, app.Name, "repo", "kept", app.RepoDir)
	}
	return nil
}
