package singleserver

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func cliEnv(args []string, w io.Writer) error {
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
		fmt.Fprintf(w, "%s\tenv\tok\tset %s\n", app.Name, key)
		fmt.Fprintf(w, "%s\tnext\tdeploy with `singleserver deploy %s`\n", app.Name, app.Repo)
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
		fmt.Fprintf(w, "%s\tenv\tok\tunset %s\n", app.Name, key)
		fmt.Fprintf(w, "%s\tnext\tdeploy with `singleserver deploy %s`\n", app.Name, app.Repo)
	default:
		return fmt.Errorf("unknown env command %q", command)
	}
	return nil
}

func cliStorage(args []string, w io.Writer, logger *log.Logger) error {
	if len(args) == 0 {
		return errors.New("usage: singleserver storage enable <app> [--mount /storage] [--path /srv/storage/app] [--no-deploy]")
	}
	switch args[0] {
	case "enable":
		return cliStorageEnable(args[1:], w, logger)
	default:
		return fmt.Errorf("unknown storage command %q", args[0])
	}
}

func cliStorageEnable(args []string, w io.Writer, logger *log.Logger) error {
	fs := flag.NewFlagSet("storage enable", flag.ContinueOnError)
	fs.SetOutput(w)
	mount := fs.String("mount", "/storage", "container mount path")
	path := fs.String("path", "", "host storage path")
	noDeploy := fs.Bool("no-deploy", false, "update config without deploying")
	if err := fs.Parse(normalizeFlagArgs(args, storageFlagTakesValue)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: singleserver storage enable <app> [--mount /storage] [--path /srv/storage/app] [--no-deploy]")
	}
	appName := fs.Arg(0)

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
	_ = commandRun(3*time.Second, "chown", "-R", "deploy:docker", storagePath)
	if err := writeConfigFunc(configPath, config); err != nil {
		if createdStorage {
			_ = os.Remove(storagePath)
		}
		return err
	}
	fmt.Fprintf(w, "%s\tstorage\tok\t%s:%s\n", app.Name, storagePath, storageMount)

	app, err = configuredApp(appName)
	if err != nil {
		return err
	}
	if *noDeploy {
		fmt.Fprintf(w, "%s\tnext\tdeploy with `singleserver deploy %s`\n", app.Name, app.Repo)
		return nil
	}
	fmt.Fprintf(w, "%s\tdeploy\tstart\tapplying storage change\n", app.Name)
	if err := cliDeploy([]string{app.Repo}, w, logger); err != nil {
		return err
	}
	return cliDoctor([]string{app.Name}, w)
}

func cliBackup(args []string, w io.Writer) error {
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
	fmt.Fprintf(w, "%s\tbackup\tok\t%s\tfiles=%d\tsqlite=%d\n", app.Name, result.Path, result.Files, result.SQLiteFiles)
	return nil
}

func cliRestore(args []string, w io.Writer) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	fs.SetOutput(w)
	yes := fs.Bool("yes", false, "confirm destructive restore")
	noRestart := fs.Bool("no-restart", false, "restore files without restarting app containers")
	if err := fs.Parse(normalizeFlagArgs(args, noFlagValues)); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return errors.New("usage: singleserver restore <app> <backup-id-or-path> --yes [--no-restart]")
	}
	if !*yes {
		return errors.New("restore replaces app storage; rerun with --yes to confirm")
	}
	app, err := configuredApp(fs.Arg(0))
	if err != nil {
		return err
	}
	storage, err := requireStorage(app)
	if err != nil {
		return err
	}
	backupPath := resolveBackupPath(app.Name, fs.Arg(1))
	if err := restoreStorageBackup(app.Name, storage.Path, backupPath, *noRestart, w); err != nil {
		return err
	}
	return nil
}

func cliRemove(args []string, w io.Writer) error {
	fs := flag.NewFlagSet("remove", flag.ContinueOnError)
	fs.SetOutput(w)
	deleteStorage := fs.Bool("delete-storage", false, "delete persistent storage")
	deleteRepo := fs.Bool("delete-repo", false, "delete repository checkout")
	yes := fs.Bool("yes", false, "confirm destructive deletion")
	if err := fs.Parse(normalizeFlagArgs(args, noFlagValues)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: singleserver remove <app> [--delete-storage] [--delete-repo] [--yes]")
	}
	if (*deleteStorage || *deleteRepo) && !*yes {
		return errors.New("remove deletes app files; rerun with --yes to confirm")
	}
	appName := fs.Arg(0)
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
	fmt.Fprintf(w, "%s\tconfig\tok\tremoved from %s\n", app.Name, configPath)
	if err := stopAppContainersFunc(app.Name); err != nil {
		fmt.Fprintf(w, "%s\tcontainers\tfailed\t%s\n", app.Name, err)
		return err
	} else {
		fmt.Fprintf(w, "%s\tcontainers\tok\tstopped matching containers\n", app.Name)
	}
	if *deleteStorage && app.Storage != nil {
		if err := os.RemoveAll(app.Storage.Path); err != nil {
			return err
		}
		fmt.Fprintf(w, "%s\tstorage\tok\tdeleted %s\n", app.Name, app.Storage.Path)
	} else if app.Storage != nil {
		fmt.Fprintf(w, "%s\tstorage\tkept\t%s\n", app.Name, app.Storage.Path)
	}
	if *deleteRepo {
		if err := os.RemoveAll(app.RepoDir); err != nil {
			return err
		}
		fmt.Fprintf(w, "%s\trepo\tok\tdeleted %s\n", app.Name, app.RepoDir)
	} else {
		fmt.Fprintf(w, "%s\trepo\tkept\t%s\n", app.Name, app.RepoDir)
	}
	return nil
}

func cliDomains(args []string, w io.Writer, logger *log.Logger) error {
	if len(args) == 0 {
		return errors.New("usage: singleserver domains <add|remove|list|verify> ...")
	}
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
}

func cliDomainChange(args []string, add bool, w io.Writer, logger *log.Logger) error {
	command := "add"
	if !add {
		command = "remove"
	}
	fs := flag.NewFlagSet("domains "+command, flag.ContinueOnError)
	fs.SetOutput(w)
	noDeploy := fs.Bool("no-deploy", false, "update config and routing without deploying")
	if err := fs.Parse(normalizeFlagArgs(args, noFlagValues)); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return fmt.Errorf("usage: singleserver domains %s <app> <domain> [--no-deploy]", command)
	}

	app, err := updateDomain(fs.Arg(0), fs.Arg(1), add, w)
	if err != nil {
		return err
	}
	if *noDeploy {
		fmt.Fprintf(w, "%s\tnext\tdeploy with `singleserver deploy %s`\n", app.Name, app.Repo)
		return nil
	}
	fmt.Fprintf(w, "%s\tdeploy\tstart\tapplying domain change\n", app.Name)
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
		if app.Healthcheck == "" {
			app.Healthcheck = "https://" + host + app.HealthcheckPath
		}
	} else {
		removedHealthcheck := "https://" + host + app.HealthcheckPath
		app.Hosts = removeFold(app.Hosts, host)
		if strings.EqualFold(app.Healthcheck, removedHealthcheck) {
			if len(app.Hosts) > 0 {
				app.Healthcheck = "https://" + app.Hosts[0] + app.HealthcheckPath
			} else {
				app.Healthcheck = ""
			}
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
		fmt.Fprintf(w, "%s\tdomain\tok\tadded %s\n", app.Name, host)
	} else {
		fmt.Fprintf(w, "%s\tdomain\tok\tremoved %s\n", app.Name, host)
	}
	return app, nil
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
	routes := map[string]string{}
	failed := false
	if state.TunnelID == "" {
		if appsHaveHosts(apps) {
			fmt.Fprintln(w, "cloudflare\tfailed\tconnect Cloudflare first with `singleserver cloudflare connect`")
			failed = true
		}
	} else if state.ConfigFile == "" {
		fmt.Fprintln(w, "cloudflare\troutes\tfailed\tmissing config file")
		failed = true
	} else {
		cloudflaredConfig, err := readCloudflaredConfig(state.ConfigFile)
		if err != nil {
			fmt.Fprintf(w, "cloudflare\troutes\tfailed\t%s\n", err)
			failed = true
		} else {
			routes = cloudflaredRoutes(cloudflaredConfig)
		}
	}

	for _, app := range apps {
		for _, host := range app.Hosts {
			if err := commandRun(5*time.Second, "getent", "hosts", host); err != nil {
				fmt.Fprintf(w, "%s\tdns\tfailed\t%s\t%s\n", app.Name, host, err)
				failed = true
			} else {
				fmt.Fprintf(w, "%s\tdns\tok\t%s\n", app.Name, host)
			}
			if state.TunnelID == "" || state.ConfigFile == "" {
				continue
			}
			if service := routes[strings.ToLower(host)]; service == "" {
				fmt.Fprintf(w, "%s\ttunnel_route\tfailed\t%s missing from %s\n", app.Name, host, state.ConfigFile)
				failed = true
			} else {
				fmt.Fprintf(w, "%s\ttunnel_route\tok\t%s -> %s\n", app.Name, host, service)
			}
		}
	}
	if failed {
		return errors.New("domain verification failed")
	}
	return nil
}

func cliUpgrade(w io.Writer) error {
	installURL := envDefault("SINGLESERVER_INSTALL_URL", "https://singleserver.com/install.sh")
	if err := commandRunFunc(10*time.Minute, "bash", "-lc", "curl -fsSL "+shellQuote(installURL)+" | sh"); err != nil {
		return err
	}
	if err := commandRunFunc(15*time.Second, "systemctl", "restart", "singleserver.service"); err != nil {
		return err
	}
	fmt.Fprintln(w, "upgrade\tok\tinstaller completed")
	return cliDoctor(nil, w)
}

func configuredApp(appName string) (*AppConfig, error) {
	config, err := LoadConfig(envDefault("SINGLESERVER_CONFIG", "/etc/singleserver/apps.yml"))
	if err != nil {
		return nil, err
	}
	for i := range config.Apps {
		if appMatches(config.Apps[i], appName) {
			return &config.Apps[i], nil
		}
	}
	return nil, fmt.Errorf("%s is not configured", appName)
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

var stopAppContainersFunc = stopAppContainers

func stopAppContainers(appName string) error {
	out, err := commandOutput(5*time.Second, "docker", "ps", "-a", "--format", "{{.Names}}")
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
	return commandRun(30*time.Second, "docker", args...)
}

func stopRunningAppContainers(appName string) ([]string, error) {
	out, err := commandOutput(5*time.Second, "docker", "ps", "--format", "{{.Names}}")
	if err != nil {
		return nil, err
	}
	names := matchingAppContainerNames(appName, out)
	if len(names) == 0 {
		return nil, nil
	}
	args := append([]string{"stop"}, names...)
	if err := commandRun(30*time.Second, "docker", args...); err != nil {
		return nil, err
	}
	return names, nil
}

func startContainers(names []string) error {
	if len(names) == 0 {
		return nil
	}
	args := append([]string{"start"}, names...)
	return commandRun(30*time.Second, "docker", args...)
}

func appContainerName(appName string) (string, error) {
	out, err := commandOutput(5*time.Second, "docker", "ps", "--format", "{{.Names}}")
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
	out, err := commandOutput(5*time.Second, "docker", "ps", "--format", "{{.Names}}")
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
