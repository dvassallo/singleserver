package singleserver

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"strconv"
	"strings"
)

var (
	editPromptInput           io.Reader = addPromptInput
	editPromptInteractiveFunc           = defaultAddPromptInteractive
)

type editOptions struct {
	appSettings
	appName string

	dockerfile     bool
	noHealthcheck  bool
	noDeploy       bool
	nonInteractive bool
}

type editPromptContext struct {
	hasDockerfile bool
	targetBranch  string
}

const editUsage = "usage: singleserver edit <app|owner/repo|github-url> [options]"

func cliEdit(args []string, w io.Writer, logger *log.Logger) error {
	opts, err := parseEditArgs(args, w)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	configPath := envDefault("SINGLESERVER_CONFIG", "/etc/singleserver/apps.yml")
	config, err := LoadConfig(configPath)
	if err != nil {
		return err
	}
	appIndex := -1
	for i := range config.Apps {
		if appMatches(config.Apps[i], opts.appName) {
			appIndex = i
			break
		}
	}
	if appIndex < 0 {
		return fmt.Errorf("%s is not configured", opts.appName)
	}

	original := config.Apps[appIndex]
	if editPromptInteractiveFunc() && !opts.nonInteractive && !opts.hasSettingFlags() {
		ctx, err := inspectEditRepo(original)
		if err != nil {
			return err
		}
		opts, err = promptEditOptions(original, opts, editPromptInput, w, ctx)
		if err != nil {
			return err
		}
	} else if !opts.hasSettingFlags() {
		return errors.New(editUsage)
	}

	app, err := applyEditOptions(original, opts)
	if err != nil {
		return err
	}
	config.Apps[appIndex] = app
	if err := config.Normalize(); err != nil {
		return err
	}
	app = config.Apps[appIndex]
	if err := writeConfigFunc(configPath, config); err != nil {
		return err
	}
	writeCheck(w, app.Name, "config", "ok", configPath, "updated")

	if opts.noDeploy {
		writeCheck(w, app.Name, "next", "pending", "deploy with `singleserver deploy "+app.Repo+"`")
		return nil
	}
	writeCheck(w, app.Name, "deploy", "start", "applying config change")
	if err := cliDeploy([]string{app.Repo}, w, logger); err != nil {
		return err
	}
	return cliDoctor([]string{app.Name}, w)
}

func parseEditArgs(args []string, w io.Writer) (editOptions, error) {
	var opts editOptions
	mode, args, err := commandModeFromArgs(args, editFlagTakesValue)
	if err != nil {
		return editOptions{}, err
	}
	opts.nonInteractive = mode.NonInteractive

	fs := flag.NewFlagSet("edit", flag.ContinueOnError)
	fs.SetOutput(w)
	fs.BoolVar(&opts.noHealthcheck, "no-healthcheck", false, "clear external healthcheck URL")
	fs.BoolVar(&opts.dockerfile, "dockerfile", false, "use the repository Dockerfile and clear generated runtime settings")
	fs.BoolVar(&opts.noDeploy, "no-deploy", false, "update config without deploying")

	appPort := bindAppSettingsFlags(fs, &opts.appSettings)
	if err := fs.Parse(normalizeFlagArgs(args, editFlagTakesValue)); err != nil {
		return editOptions{}, err
	}
	opts.appPort = *appPort
	fs.Visit(func(f *flag.Flag) {
		noteAppSettingsFlag(&opts.appSettings, f.Name)
	})

	if fs.NArg() != 1 {
		return editOptions{}, errors.New(editUsage)
	}
	appName, err := normalizeRepoArg(fs.Arg(0))
	if err != nil {
		return editOptions{}, err
	}
	opts.appName = appName
	if opts.dockerfile && opts.runtimeSet {
		return editOptions{}, errors.New("--dockerfile and --runtime cannot be used together")
	}
	if opts.noHealthcheck && opts.healthcheckSet {
		return editOptions{}, errors.New("--healthcheck and --no-healthcheck cannot be used together")
	}
	return opts, nil
}

func editFlagTakesValue(arg string) bool {
	return appSettingsFlagTakesValue(arg)
}

func (o editOptions) hasSettingFlags() bool {
	return o.branchSet ||
		o.healthcheckSet ||
		o.noHealthcheck ||
		o.healthcheckPathSet ||
		o.dockerfile ||
		o.runtimeSet ||
		o.installSet ||
		o.buildSet ||
		o.startSet ||
		o.staticDirSet ||
		o.appPortSet
}

func applyEditOptions(app AppConfig, opts editOptions) (AppConfig, error) {
	return applyAppSettings(app, opts.appSettings, opts.dockerfile, opts.noHealthcheck)
}

func inspectEditRepo(app AppConfig) (editPromptContext, error) {
	github := NewGitHubClient(envDefault("SINGLESERVER_STATE_DIR", "/etc/singleserver"))
	installationID, err := github.RepositoryInstallationID(app.Repo)
	if err != nil {
		return editPromptContext{}, err
	}
	token, err := github.DeployToken(installationID)
	if err != nil {
		return editPromptContext{}, err
	}
	defaultBranch, err := github.RepositoryDefaultBranch(app.Repo, token)
	if err != nil {
		return editPromptContext{}, err
	}
	targetBranch := app.Branch
	if targetBranch == "" {
		targetBranch = defaultBranch
	}
	hasDockerfile, err := github.RepositoryFileExists(app.Repo, "Dockerfile", targetBranch, token)
	if err != nil {
		return editPromptContext{}, err
	}
	return editPromptContext{hasDockerfile: hasDockerfile, targetBranch: targetBranch}, nil
}

func promptEditOptions(app AppConfig, opts editOptions, input io.Reader, w io.Writer, ctx editPromptContext) (editOptions, error) {
	p := addPrompter{reader: bufio.NewReader(input), w: w}
	fmt.Fprintf(w, "Editing %s (%s on %s).\n", app.Name, app.Repo, ctx.targetBranch)

	mode, err := promptEditBuildMode(app, p, ctx)
	if err != nil {
		return editOptions{}, err
	}
	switch mode {
	case "dockerfile":
		opts.dockerfile = true
		if !ctx.hasDockerfile {
			return editOptions{}, fmt.Errorf("%s does not have a Dockerfile on %s", app.Repo, ctx.targetBranch)
		}
		if err := promptEditDockerfileOptions(app, &opts, p); err != nil {
			return editOptions{}, err
		}
	case "static", "node", "bun":
		opts.runtime = mode
		opts.runtimeSet = true
		if err := promptEditGeneratedOptions(app, &opts, p); err != nil {
			return editOptions{}, err
		}
	}

	currentPath := strings.TrimSpace(app.HealthcheckPath)
	if currentPath == "" {
		currentPath = promptReadinessDefault(opts.runtime, opts.staticDir)
	}
	readinessPath, err := p.askDefault("Readiness path", currentPath)
	if err != nil {
		return editOptions{}, err
	}
	opts.healthcheckPath = readinessPath
	opts.healthcheckPathSet = true

	healthcheck, cleared, err := p.askOptionalEdit("External healthcheck URL", app.Healthcheck)
	if err != nil {
		return editOptions{}, err
	}
	if cleared {
		opts.noHealthcheck = true
	} else {
		opts.healthcheck = healthcheck
		opts.healthcheckSet = true
	}

	deploy, err := p.askYesNo("Deploy now?", true)
	if err != nil {
		return editOptions{}, err
	}
	opts.noDeploy = !deploy

	fmt.Fprintf(w, "Equivalent command:\n  %s\n", editEquivalentCommand(app.Name, opts))
	return opts, nil
}

func promptEditBuildMode(app AppConfig, p addPrompter, ctx editPromptContext) (string, error) {
	current := strings.TrimSpace(app.Runtime)
	if current == "" {
		current = "dockerfile"
	}
	if ctx.hasDockerfile {
		return p.askChoiceDefault("Build mode", []string{"dockerfile"}, "dockerfile")
	}
	if current == "dockerfile" {
		current = "static"
	}
	return p.askChoiceDefault("Build mode", []string{"static", "node", "bun"}, current)
}

func promptEditDockerfileOptions(app AppConfig, opts *editOptions, p addPrompter) error {
	port, err := p.askPortDefault("App port", app.AppPort)
	if err != nil {
		return err
	}
	opts.appPort = port
	opts.appPortSet = true
	return nil
}

func promptEditGeneratedOptions(app AppConfig, opts *editOptions, p addPrompter) error {
	switch opts.runtime {
	case "static":
		current := app.StaticDir
		if current == "" {
			current = "."
		}
		staticDir, err := p.askDefault("Static directory", current)
		if err != nil {
			return err
		}
		opts.staticDir = staticDir
		opts.staticDirSet = true
	case "node", "bun":
		installCommand, cleared, err := p.askOptionalEdit("Install command", app.InstallCommand)
		if err != nil {
			return err
		}
		if cleared || installCommand != "" {
			opts.installCommand = installCommand
			opts.installSet = true
		}
		buildCommand, cleared, err := p.askOptionalEdit("Build command", app.BuildCommand)
		if err != nil {
			return err
		}
		if cleared || buildCommand != "" {
			opts.buildCommand = buildCommand
			opts.buildSet = true
		}

		defaultOutputMode := "process"
		if app.StaticDir != "" {
			defaultOutputMode = "static"
		}
		outputMode, err := p.askChoiceDefault("Output mode", []string{"static", "process"}, defaultOutputMode)
		if err != nil {
			return err
		}
		if outputMode == "static" {
			current := app.StaticDir
			if current == "" {
				current = "dist"
			}
			staticDir, err := p.askDefault("Static output directory", current)
			if err != nil {
				return err
			}
			opts.staticDir = staticDir
			opts.staticDirSet = true
			opts.startCommand = ""
			opts.startSet = true
			return nil
		}

		opts.staticDir = ""
		opts.staticDirSet = true
		startCommand, err := p.askRequiredDefault("Start command", app.StartCommand)
		if err != nil {
			return err
		}
		opts.startCommand = startCommand
		opts.startSet = true
		defaultPort := app.AppPort
		if defaultPort == 0 || defaultPort == 80 {
			defaultPort = 3000
		}
		port, err := p.askPortDefault("App port", defaultPort)
		if err != nil {
			return err
		}
		opts.appPort = port
		opts.appPortSet = true
	}
	return nil
}

func (p addPrompter) askChoiceDefault(label string, values []string, defaultValue string) (string, error) {
	allowed := map[string]bool{}
	for _, value := range values {
		allowed[value] = true
	}
	for {
		value, err := p.askDefault(label+" ("+strings.Join(values, "/")+")", defaultValue)
		if err != nil {
			return "", err
		}
		value = strings.ToLower(strings.TrimSpace(value))
		if allowed[value] {
			return value, nil
		}
		fmt.Fprintf(p.w, "Enter one of: %s\n", strings.Join(values, ", "))
	}
}

func (p addPrompter) askOptionalEdit(label, current string) (value string, cleared bool, err error) {
	display := current
	if strings.TrimSpace(display) == "" {
		display = "none"
	}
	answer, err := p.ask(label+" (- for none)", display)
	if err != nil {
		return "", false, err
	}
	if answer == "" {
		return strings.TrimSpace(current), false, nil
	}
	if answer == "-" {
		return "", true, nil
	}
	return answer, false, nil
}

func (p addPrompter) askRequiredDefault(label, current string) (string, error) {
	for {
		value, err := p.askDefault(label, current)
		if err != nil {
			return "", err
		}
		if value != "" {
			return value, nil
		}
		fmt.Fprintln(p.w, "This value is required.")
	}
}

func (p addPrompter) askPortDefault(label string, current int) (int, error) {
	if current <= 0 {
		current = 80
	}
	for {
		value, err := p.askDefault(label, strconv.Itoa(current))
		if err != nil {
			return 0, err
		}
		port, parseErr := strconv.Atoi(value)
		if parseErr == nil && port >= 1 && port <= 65535 {
			return port, nil
		}
		fmt.Fprintln(p.w, "Enter a port from 1 to 65535.")
	}
}

func editEquivalentCommand(appName string, opts editOptions) string {
	parts := []string{"singleserver", "edit", shellQuote(appName)}

	if opts.dockerfile {
		parts = append(parts, "--dockerfile")
	}
	parts = appendAppSettingsFlags(parts, opts.appSettings, true)
	if opts.noHealthcheck {
		parts = append(parts, "--no-healthcheck")
	}
	if opts.noDeploy {
		parts = append(parts, "--no-deploy")
	}
	parts = append(parts, "--non-interactive")
	return strings.Join(parts, " ")
}
