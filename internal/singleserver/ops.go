package singleserver

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
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

func cliStorage(args []string, w io.Writer, logger *log.Logger) error {
	mode, args, err := commandModeFromArgs(args, noFlagValues)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return errors.New("usage: singleserver storage enable <app> [--mount /storage] [--path /srv/storage/app] [--no-deploy]")
	}
	return withCLIMode(mode, func() error {
		switch args[0] {
		case "enable":
			return cliStorageEnable(args[1:], w, logger)
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
	appIndex := -1
	for i := range config.Apps {
		if appMatches(config.Apps[i], appName) {
			appIndex = i
			break
		}
	}
	if appIndex == -1 {
		return fmt.Errorf("%s is not configured", appName)
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
		writeCheck(w, app.Name, "next", "pending", "deploy with `singleserver deploy "+app.Repo+"`")
		return nil
	}
	writeCheck(w, app.Name, "deploy", "start", "applying storage change")
	if err := cliDeploy([]string{app.Repo}, w, logger); err != nil {
		return err
	}
	return cliDoctor([]string{app.Name}, w)
}

func cliBackup(args []string, w io.Writer) error {
	mode, args, err := commandModeFromArgs(args, noFlagValues)
	if err != nil {
		return err
	}
	if len(args) == 0 && cliCanPrompt(mode) {
		appName, err := promptConfiguredAppName(interactivePrompter(w), "App to back up")
		if err != nil {
			return err
		}
		args = append(args, appName)
	}
	if len(args) != 1 {
		return errors.New("usage: singleserver backup <app>")
	}
	app, err := configuredApp(args[0])
	if err != nil {
		return err
	}
	storage, err := requireStorage(app)
	if err != nil {
		return err
	}
	result, err := createStorageBackup(app.Name, storage.Path, filepath.Join(backupRoot(), app.Name))
	if err != nil {
		return err
	}
	writeCheck(w, app.Name, "backup", "ok", result.Path, fmt.Sprintf("files=%d", result.Files), fmt.Sprintf("sqlite=%d", result.SQLiteFiles))
	return nil
}

func cliRestore(args []string, w io.Writer) error {
	mode, args, err := commandModeFromArgs(args, noFlagValues)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	fs.SetOutput(w)
	noRestart := fs.Bool("no-restart", false, "restore files without restarting app containers")
	if err := fs.Parse(normalizeFlagArgs(args, noFlagValues)); err != nil {
		return err
	}
	prompting := cliCanPrompt(mode)
	p := interactivePrompter(w)
	restoreArgs := fs.Args()
	if len(restoreArgs) == 0 && prompting {
		appName, err := promptConfiguredAppName(p, "App to restore")
		if err != nil {
			return err
		}
		restoreArgs = append(restoreArgs, appName)
	}
	if len(restoreArgs) == 1 && prompting {
		backup, err := p.askRequired("Backup id or path")
		if err != nil {
			return err
		}
		restoreArgs = append(restoreArgs, backup)
	}
	if len(restoreArgs) != 2 {
		return errors.New("usage: singleserver restore <app> <backup-id-or-path> [--no-restart]")
	}
	app, err := configuredApp(restoreArgs[0])
	if err != nil {
		return err
	}
	storage, err := requireStorage(app)
	if err != nil {
		return err
	}
	backupPath := resolveBackupPath(app.Name, restoreArgs[1])
	if prompting {
		fmt.Fprintf(w, "Restore %s from %s.\n", app.Name, backupPath)
		fmt.Fprintf(w, "Current storage will move aside before the backup is restored: %s\n", storage.Path)
		proceed, err := p.askYesNo("Continue?", false)
		if err != nil {
			return err
		}
		if !proceed {
			writeCheck(w, app.Name, "restore", "canceled", backupPath)
			return nil
		}
	}
	if err := restoreStorageBackup(app.Name, storage.Path, backupPath, *noRestart, w); err != nil {
		return err
	}
	return nil
}

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

func cliDomains(args []string, w io.Writer, logger *log.Logger) error {
	mode, args, err := commandModeFromArgs(args, noFlagValues)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return errors.New("usage: singleserver domains <add|remove|list|verify> ...")
	}
	return withCLIMode(mode, func() error {
		switch args[0] {
		case "add":
			return cliDomainChange(args[1:], true, w, logger)
		case "remove":
			return cliDomainChange(args[1:], false, w, logger)
		case "list":
			if len(args) > 2 {
				return errors.New("usage: singleserver domains list [app]")
			}
			return listDomains(args[1:], w)
		case "verify":
			if len(args) > 2 {
				return errors.New("usage: singleserver domains verify [app]")
			}
			return verifyDomains(args[1:], w)
		default:
			return fmt.Errorf("unknown domains command %q", args[0])
		}
	})
}

func cliDomainChange(args []string, add bool, w io.Writer, logger *log.Logger) error {
	command := "add"
	if !add {
		command = "remove"
	}
	mode, args, err := commandModeFromArgs(args, noFlagValues)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("domains "+command, flag.ContinueOnError)
	fs.SetOutput(w)
	noDeploy := fs.Bool("no-deploy", false, "update config and DNS without deploying")
	if err := fs.Parse(normalizeFlagArgs(args, noFlagValues)); err != nil {
		return err
	}
	prompting := cliCanPrompt(mode)
	p := interactivePrompter(w)
	changeArgs := fs.Args()
	if len(changeArgs) == 0 && prompting {
		appName, err := promptConfiguredAppName(p, "App")
		if err != nil {
			return err
		}
		changeArgs = append(changeArgs, appName)
	}
	if len(changeArgs) == 1 && prompting {
		label := "Domain to add"
		if !add {
			label = "Domain to remove"
		}
		host, err := p.askRequired(label)
		if err != nil {
			return err
		}
		changeArgs = append(changeArgs, host)
	}
	if len(changeArgs) != 2 {
		return fmt.Errorf("usage: singleserver domains %s <app> <domain> [--no-deploy]", command)
	}
	if prompting && !*noDeploy {
		deploy, err := p.askYesNo("Deploy now?", true)
		if err != nil {
			return err
		}
		*noDeploy = !deploy
	}

	app, err := updateDomain(changeArgs[0], changeArgs[1], add, w)
	if err != nil {
		return err
	}
	if *noDeploy {
		writeCheck(w, app.Name, "next", "pending", "deploy with `singleserver deploy "+app.Repo+"`")
		return nil
	}
	writeCheck(w, app.Name, "deploy", "start", "applying domain change")
	if err := cliDeploy([]string{app.Repo}, w, logger); err != nil {
		return err
	}
	return cliDoctor([]string{app.Name}, w)
}

func updateDomain(appName string, host string, add bool, w io.Writer) (*AppConfig, error) {
	host = strings.TrimSpace(host)
	if host == "" || strings.Contains(host, "://") || strings.Contains(host, "/") {
		return nil, fmt.Errorf("invalid domain: %q", host)
	}
	configPath := envDefault("SINGLESERVER_CONFIG", "/etc/singleserver/apps.yml")
	config, err := LoadConfig(configPath)
	if err != nil {
		return nil, err
	}
	appIndex := -1
	for i := range config.Apps {
		if appMatches(config.Apps[i], appName) {
			appIndex = i
			break
		}
	}
	if appIndex < 0 {
		return nil, fmt.Errorf("%s is not configured", appName)
	}

	if !add && !containsFold(config.Apps[appIndex].Hosts, host) {
		return nil, fmt.Errorf("%s is not configured for %s", host, config.Apps[appIndex].Name)
	}

	app := &config.Apps[appIndex]
	if add {
		if !containsFold(app.Hosts, host) {
			app.Hosts = append(app.Hosts, host)
		}
	} else {
		app.Hosts = removeFold(app.Hosts, host)
		if healthcheckBelongsToHost(app.Healthcheck, host, app.HealthcheckPath) {
			app.Healthcheck = ""
		}
	}
	if err := config.Normalize(); err != nil {
		return nil, err
	}
	app = &config.Apps[appIndex]

	if err := syncCloudflareAppDomainFunc(host, add, w); err != nil {
		return nil, err
	}
	if err := writeConfig(configPath, config); err != nil {
		if rollbackErr := syncCloudflareAppDomainFunc(host, !add, io.Discard); rollbackErr != nil {
			return nil, fmt.Errorf("%w; rollback cloudflare domain failed: %v", err, rollbackErr)
		}
		return nil, err
	}
	if add {
		writeCheck(w, app.Name, "domain", "ok", host, "added")
	} else {
		writeCheck(w, app.Name, "domain", "ok", host, "removed")
	}
	return app, nil
}

func healthcheckBelongsToHost(healthcheck, host, path string) bool {
	if strings.TrimSpace(healthcheck) == "" {
		return false
	}
	for _, candidatePath := range []string{path, "/up"} {
		if candidatePath == "" {
			continue
		}
		if !strings.HasPrefix(candidatePath, "/") {
			candidatePath = "/" + candidatePath
		}
		if strings.EqualFold(healthcheck, "https://"+host+candidatePath) {
			return true
		}
	}
	return false
}

func listDomains(args []string, w io.Writer) error {
	config, err := LoadConfig(envDefault("SINGLESERVER_CONFIG", "/etc/singleserver/apps.yml"))
	if err != nil {
		return err
	}
	for _, app := range config.Apps {
		if len(args) == 1 && !appMatches(app, args[0]) {
			continue
		}
		if len(app.Hosts) == 0 {
			fmt.Fprintf(w, "%s\t-\n", app.Name)
			continue
		}
		for _, host := range app.Hosts {
			fmt.Fprintf(w, "%s\t%s\n", app.Name, host)
		}
	}
	return nil
}

func verifyDomains(args []string, w io.Writer) error {
	config, err := LoadConfig(envDefault("SINGLESERVER_CONFIG", "/etc/singleserver/apps.yml"))
	if err != nil {
		return err
	}
	apps := config.Apps
	if len(args) == 1 {
		apps = nil
		for _, app := range config.Apps {
			if appMatches(app, args[0]) {
				apps = []AppConfig{app}
				break
			}
		}
		if len(apps) == 0 {
			return fmt.Errorf("%s is not configured", args[0])
		}
	}

	state, err := loadCloudflareState()
	if err != nil {
		return err
	}
	var cloudflareClient *CloudflareClient
	failed := false
	if state.TunnelID != "" {
		if token := cloudflareTokenFromEnvOrState(state); token != "" {
			client, err := newCloudflareClient(token)
			if err != nil {
				writeCheck(w, "cloudflare", "dns_api", "failed", err.Error())
				failed = true
			} else {
				cloudflareClient = client
			}
		}
	} else if appsHaveHosts(apps) {
		writeCheck(w, "cloudflare", "setup", "skipped", "-", "connect Cloudflare with `singleserver connect cloudflare` to verify DNS and tunnel routes")
	}

	verifyResolverDNS := cloudflareClient == nil
	for _, app := range apps {
		for _, host := range app.Hosts {
			if verifyResolverDNS && !doctorHostResolves(w, app.Name, "dns", host) {
				failed = true
			}
			if cloudflareClient != nil {
				target, err := verifyCloudflareDNSRecordFunc(host, state, cloudflareClient)
				if err != nil {
					writeCheck(w, app.Name, "cloudflare_dns", "failed", host, err.Error())
					failed = true
				} else {
					writeCheck(w, app.Name, "cloudflare_dns", "ok", host, "target="+target)
				}
			}
		}
	}
	if failed {
		return errors.New("domain verification failed")
	}
	return nil
}

var verifyCloudflareDNSRecordFunc = verifyCloudflareDNSRecord

func verifyCloudflareDNSRecord(host string, state *CloudflareState, client *CloudflareClient) (string, error) {
	if strings.TrimSpace(state.TunnelID) == "" {
		return "", errors.New("no Cloudflare DNS target configured; run `singleserver connect cloudflare`")
	}
	target := state.TunnelID + ".cfargotunnel.com"
	zone, err := client.zoneForHostname(host)
	if err != nil {
		return target, err
	}
	records, err := client.dnsRecords(zone.ID, host, "CNAME")
	if err != nil {
		return target, err
	}
	for _, record := range records {
		if dnsRecordContentMatches(record.Content, target) {
			return target, nil
		}
	}
	if len(records) == 0 {
		return target, fmt.Errorf("missing CNAME to %s", target)
	}
	contents := make([]string, 0, len(records))
	for _, record := range records {
		contents = append(contents, record.Content)
	}
	return target, fmt.Errorf("CNAME points to %s, expected %s", strings.Join(contents, ","), target)
}

func cliUpgrade(args []string, w io.Writer) error {
	mode, args, err := commandModeFromArgs(args, noFlagValues)
	if err != nil {
		return err
	}
	if len(args) != 0 {
		return errors.New("usage: singleserver upgrade")
	}
	if cliCanPrompt(mode) {
		proceed, err := interactivePrompter(w).askYesNo("Upgrade Single Server now?", false)
		if err != nil {
			return err
		}
		if !proceed {
			writeCheck(w, "upgrade", "installer", "canceled", "-")
			return nil
		}
	}
	if err := commandRunFunc(10*time.Minute, "bash", "-lc", "curl -fsSL https://singleserver.com/install.sh | sh"); err != nil {
		return err
	}
	if err := commandRunFunc(15*time.Second, "systemctl", "restart", "singleserver.service"); err != nil {
		return err
	}
	writeCheck(w, "upgrade", "installer", "ok", "completed")
	return cliDoctor(nil, w)
}

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

func interactivePrompter(w io.Writer) addPrompter {
	return addPrompter{reader: bufio.NewReader(addPromptInput), w: w}
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

var stopAppContainersFunc = stopAppContainers

func stopAppContainers(appName string) error {
	out, err := commandOutputFunc(5*time.Second, "docker", "ps", "-a", "--format", "{{.Names}}")
	if err != nil {
		return err
	}
	names := []string{}
	for _, name := range strings.Split(out, "\n") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if strings.HasPrefix(name, appName+"-") || name == appName {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	args := append([]string{"rm", "-f"}, names...)
	return commandRunFunc(30*time.Second, "docker", args...)
}

func stopRunningAppContainers(appName string) ([]string, error) {
	out, err := commandOutputFunc(5*time.Second, "docker", "ps", "--format", "{{.Names}}")
	if err != nil {
		return nil, err
	}
	names := matchingAppContainerNames(appName, out)
	if len(names) == 0 {
		return nil, nil
	}
	args := append([]string{"stop"}, names...)
	if err := commandRunFunc(30*time.Second, "docker", args...); err != nil {
		return nil, err
	}
	return names, nil
}

func startContainers(names []string) error {
	if len(names) == 0 {
		return nil
	}
	args := append([]string{"start"}, names...)
	return commandRunFunc(30*time.Second, "docker", args...)
}

func appContainerName(appName string) (string, error) {
	out, err := commandOutputFunc(5*time.Second, "docker", "ps", "--format", "{{.Names}}")
	if err != nil {
		return "", err
	}
	containers := map[string]string{}
	for _, name := range strings.Split(out, "\n") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		containers[name] = name
	}
	if name, ok := containerForApp(appName, containers); ok {
		return name, nil
	}
	return "", fmt.Errorf("no running container found for %s", appName)
}

func matchingAppContainerNames(appName string, output string) []string {
	names := []string{}
	for _, name := range strings.Split(output, "\n") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if strings.HasPrefix(name, appName+"-") || name == appName {
			names = append(names, name)
		}
	}
	return names
}

func runningAppContainers() (map[string]string, error) {
	out, err := commandOutputFunc(5*time.Second, "docker", "ps", "--format", "{{.Names}}")
	if err != nil {
		return nil, err
	}
	containers := map[string]string{}
	for _, name := range strings.Split(out, "\n") {
		name = strings.TrimSpace(name)
		if name != "" {
			containers[name] = name
		}
	}
	return containers, nil
}

func containerForApp(appName string, containers map[string]string) (string, bool) {
	if containers == nil {
		return "", false
	}
	if container, ok := containers[appName]; ok {
		return container, true
	}
	for name, container := range containers {
		if strings.HasPrefix(name, appName+"-") {
			return container, true
		}
	}
	return "", false
}

func runCommandToWriter(w io.Writer, timeout time.Duration, name string, args ...string) error {
	ctx := context.Background()
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = w
	cmd.Stderr = w
	err := cmd.Run()
	if timeout > 0 && ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("%s timed out", name)
	}
	return err
}

var commandRunToWriterFunc = runCommandToWriter

func containsFold(values []string, needle string) bool {
	for _, value := range values {
		if strings.EqualFold(value, needle) {
			return true
		}
	}
	return false
}

func removeFold(values []string, needle string) []string {
	filtered := values[:0]
	for _, value := range values {
		if !strings.EqualFold(value, needle) {
			filtered = append(filtered, value)
		}
	}
	return filtered
}

func storageFlagTakesValue(arg string) bool {
	name := strings.TrimLeft(arg, "-")
	if before, _, ok := strings.Cut(name, "="); ok {
		name = before
	}
	return name == "mount" || name == "path"
}

func noFlagValues(string) bool {
	return false
}
