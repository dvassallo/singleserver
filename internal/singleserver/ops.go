package singleserver

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
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
	default:
		return fmt.Errorf("unknown env command %q", command)
	}
	return nil
}

func cliStorage(args []string, w io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: singleserver storage enable <app> [--mount /storage] [--path /srv/storage/app]")
	}
	switch args[0] {
	case "enable":
		return cliStorageEnable(args[1:], w)
	default:
		return fmt.Errorf("unknown storage command %q", args[0])
	}
}

func cliStorageEnable(args []string, w io.Writer) error {
	fs := flag.NewFlagSet("storage enable", flag.ContinueOnError)
	fs.SetOutput(w)
	mount := fs.String("mount", "/storage", "container mount path")
	path := fs.String("path", "", "host storage path")
	if err := fs.Parse(normalizeFlagArgs(args, storageFlagTakesValue)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: singleserver storage enable <app> [--mount /storage] [--path /srv/storage/app]")
	}
	appName := fs.Arg(0)
	return updateConfiguredApp(appName, func(app *AppConfig) error {
		storage := &StorageConfig{Path: strings.TrimSpace(*path), Mount: strings.TrimSpace(*mount)}
		app.Storage = storage
		if err := app.Normalize(); err != nil {
			return err
		}
		if err := os.MkdirAll(app.Storage.Path, 0700); err != nil {
			return err
		}
		_ = commandRun(3*time.Second, "chown", "-R", "deploy:docker", app.Storage.Path)
		fmt.Fprintf(w, "%s\tstorage\tok\t%s:%s\n", app.Name, app.Storage.Path, app.Storage.Mount)
		return nil
	})
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
	backupDir := filepath.Join("/srv/backups", app.Name)
	if err := os.MkdirAll(backupDir, 0700); err != nil {
		return err
	}
	backupID := time.Now().UTC().Format("20060102T150405Z")
	backupPath := filepath.Join(backupDir, backupID+".tar.gz")
	if err := commandRun(10*time.Minute, "tar", "-C", storage.Path, "-czf", backupPath, "."); err != nil {
		return err
	}
	fmt.Fprintf(w, "%s\tbackup\tok\t%s\n", app.Name, backupPath)
	return nil
}

func cliRestore(args []string, w io.Writer) error {
	if len(args) != 2 {
		return errors.New("usage: singleserver restore <app> <backup-id-or-path>")
	}
	app, err := configuredApp(args[0])
	if err != nil {
		return err
	}
	storage, err := requireStorage(app)
	if err != nil {
		return err
	}
	backupPath := args[1]
	if !strings.Contains(backupPath, "/") {
		backupPath = filepath.Join("/srv/backups", app.Name, backupPath+".tar.gz")
	}
	if _, err := os.Stat(backupPath); err != nil {
		return err
	}
	if err := os.MkdirAll(storage.Path, 0700); err != nil {
		return err
	}
	if err := commandRun(10*time.Minute, "tar", "-C", storage.Path, "-xzf", backupPath); err != nil {
		return err
	}
	fmt.Fprintf(w, "%s\trestore\tok\t%s\n", app.Name, backupPath)
	return nil
}

func cliRemove(args []string, w io.Writer) error {
	fs := flag.NewFlagSet("remove", flag.ContinueOnError)
	fs.SetOutput(w)
	deleteStorage := fs.Bool("delete-storage", false, "delete persistent storage")
	if err := fs.Parse(normalizeFlagArgs(args, noFlagValues)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: singleserver remove <app> [--delete-storage]")
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
	config.Apps = append(config.Apps[:index], config.Apps[index+1:]...)
	if err := writeConfig(configPath, config); err != nil {
		return err
	}
	fmt.Fprintf(w, "%s\tconfig\tok\tremoved from %s\n", app.Name, configPath)
	for _, host := range app.Hosts {
		if err := syncCloudflareAppDomain(host, false, w); err != nil {
			return err
		}
	}
	if err := stopAppContainers(app.Name); err != nil {
		fmt.Fprintf(w, "%s\tcontainers\tfailed\t%s\n", app.Name, err)
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
	return nil
}

func cliDomains(args []string, w io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: singleserver domains <add|remove|list|verify> ...")
	}
	switch args[0] {
	case "add":
		if len(args) != 3 {
			return errors.New("usage: singleserver domains add <app> <domain>")
		}
		return updateDomain(args[1], args[2], true, w)
	case "remove":
		if len(args) != 3 {
			return errors.New("usage: singleserver domains remove <app> <domain>")
		}
		return updateDomain(args[1], args[2], false, w)
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

func updateDomain(appName string, host string, add bool, w io.Writer) error {
	host = strings.TrimSpace(host)
	if host == "" || strings.Contains(host, "://") || strings.Contains(host, "/") {
		return fmt.Errorf("invalid domain: %q", host)
	}
	if err := updateConfiguredApp(appName, func(app *AppConfig) error {
		if add {
			if !containsFold(app.Hosts, host) {
				app.Hosts = append(app.Hosts, host)
			}
			if app.Healthcheck == "" {
				app.Healthcheck = "https://" + host + app.HealthcheckPath
			}
			fmt.Fprintf(w, "%s\tdomain\tok\tadded %s\n", app.Name, host)
		} else {
			app.Hosts = removeFold(app.Hosts, host)
			fmt.Fprintf(w, "%s\tdomain\tok\tremoved %s\n", app.Name, host)
		}
		return nil
	}); err != nil {
		return err
	}
	return syncCloudflareAppDomain(host, add, w)
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
	failed := false
	for _, app := range config.Apps {
		if len(args) == 1 && !appMatches(app, args[0]) {
			continue
		}
		for _, host := range app.Hosts {
			if err := commandRun(5*time.Second, "getent", "hosts", host); err != nil {
				fmt.Fprintf(w, "%s\tdns\tfailed\t%s\t%s\n", app.Name, host, err)
				failed = true
			} else {
				fmt.Fprintf(w, "%s\tdns\tok\t%s\n", app.Name, host)
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
	if err := commandRun(10*time.Minute, "bash", "-lc", "curl -fsSL "+shellQuote(installURL)+" | sh"); err != nil {
		return err
	}
	_ = commandRun(15*time.Second, "systemctl", "restart", "singleserver.service")
	fmt.Fprintln(w, "upgrade\tok\tinstaller completed")
	return cliDoctor(w)
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
		return writeConfig(configPath, config)
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

func appContainerName(appName string) (string, error) {
	out, err := commandOutput(5*time.Second, "docker", "ps", "--format", "{{.Names}}")
	if err != nil {
		return "", err
	}
	for _, name := range strings.Split(out, "\n") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if strings.HasPrefix(name, appName+"-") || name == appName {
			return name, nil
		}
	}
	return "", fmt.Errorf("no running container found for %s", appName)
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
