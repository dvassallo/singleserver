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
	appName string

	branch             string
	healthcheck        string
	healthcheckPath    string
	runtime            string
	installCommand     string
	buildCommand       string
	startCommand       string
	staticDir          string
	appPort            int
	dockerfile         bool
	noHealthcheck      bool
	noDeploy           bool
	branchSet          bool
	healthcheckSet     bool
	healthcheckPathSet bool
	runtimeSet         bool
	installSet         bool
	buildSet           bool
	startSet           bool
	staticDirSet       bool
	appPortSet         bool
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
	if editPromptInteractiveFunc() && !opts.hasSettingFlags() {
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
	fs := flag.NewFlagSet("edit", flag.ContinueOnError)
	fs.SetOutput(w)
	fs.StringVar(&opts.branch, "branch", "", "branch override")
	fs.StringVar(&opts.healthcheck, "healthcheck", "", "external healthcheck URL")
	fs.BoolVar(&opts.noHealthcheck, "no-healthcheck", false, "clear external healthcheck URL")
	fs.StringVar(&opts.healthcheckPath, "healthcheck-path", "", "container readiness path for generated Kamal config")
	fs.BoolVar(&opts.dockerfile, "dockerfile", false, "use the repository Dockerfile and clear generated runtime settings")
	fs.StringVar(&opts.runtime, "runtime", "", "generated Dockerfile runtime: static, node, or bun")
	fs.StringVar(&opts.installCommand, "install", "", "install command for generated Node/Bun Dockerfile")
	fs.StringVar(&opts.buildCommand, "build", "", "build command for generated Node/Bun Dockerfile")
	fs.StringVar(&opts.startCommand, "start", "", "start command for generated Node/Bun Dockerfile")
	fs.StringVar(&opts.staticDir, "static-dir", "", "static output directory for generated Dockerfile")
	fs.BoolVar(&opts.noDeploy, "no-deploy", false, "update config without deploying")

	appPort := fs.Int("app-port", 0, "container app port for generated Kamal config")
	if err := fs.Parse(normalizeFlagArgs(args, editFlagTakesValue)); err != nil {
		return editOptions{}, err
	}
	opts.appPort = *appPort
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "branch":
			opts.branchSet = true
		case "healthcheck":
			opts.healthcheckSet = true
		case "healthcheck-path":
			opts.healthcheckPathSet = true
		case "runtime":
			opts.runtimeSet = true
		case "install":
			opts.installSet = true
		case "build":
			opts.buildSet = true
		case "start":
			opts.startSet = true
		case "static-dir":
			opts.staticDirSet = true
		case "app-port":
			opts.appPortSet = true
		}
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
	name := strings.TrimLeft(arg, "-")
	if before, _, ok := strings.Cut(name, "="); ok {
		name = before
	}
	switch name {
	case "branch", "healthcheck", "healthcheck-path", "runtime", "install", "build", "start", "static-dir", "app-port":
		return true
	default:
		return false
	}
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
	if opts.branchSet {
		app.Branch = opts.branch
	}
	if opts.dockerfile {
		clearGeneratedRuntime(&app)
		if !opts.healthcheckPathSet {
			app.HealthcheckPath = ""
		}
	}
	if opts.runtimeSet {
		clearGeneratedRuntime(&app)
		app.Runtime = opts.runtime
		if !opts.healthcheckPathSet {
			app.HealthcheckPath = ""
		}
	}
	if opts.installSet {
		app.InstallCommand = opts.installCommand
	}
	if opts.buildSet {
		app.BuildCommand = opts.buildCommand
	}
	if opts.startSet {
		app.StartCommand = opts.startCommand
	}
	if opts.staticDirSet {
		app.StaticDir = opts.staticDir
	}
	if opts.appPortSet {
		app.AppPort = opts.appPort
		app.AppPortSet = true
	}
	if app.Runtime != "" && (app.Runtime == "static" || app.StaticDir != "") && !opts.appPortSet {
		app.AppPort = 80
		app.AppPortSet = false
	}
	if opts.healthcheckPathSet {
		app.HealthcheckPath = opts.healthcheckPath
	}
	if opts.noHealthcheck {
		app.Healthcheck = ""
	}
	if opts.healthcheckSet {
		app.Healthcheck = opts.healthcheck
	}
	if err := app.Normalize(); err != nil {
		return AppConfig{}, err
	}
	return app, nil
}

func clearGeneratedRuntime(app *AppConfig) {
	app.Runtime = ""
	app.InstallCommand = ""
	app.BuildCommand = ""
	app.StartCommand = ""
	app.StaticDir = ""
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
		currentPath = promptReadinessDefault(addOptions{runtime: opts.runtime, staticDir: opts.staticDir})
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
	appendFlagValue := func(name, value string) {
		if strings.TrimSpace(value) != "" {
			parts = append(parts, name, shellQuote(value))
		}
	}

	if opts.branchSet {
		appendFlagValue("--branch", opts.branch)
	}
	if opts.dockerfile {
		parts = append(parts, "--dockerfile")
	}
	if opts.runtimeSet {
		appendFlagValue("--runtime", opts.runtime)
	}
	if opts.installSet {
		appendFlagValue("--install", opts.installCommand)
	}
	if opts.buildSet {
		appendFlagValue("--build", opts.buildCommand)
	}
	if opts.startSet {
		appendFlagValue("--start", opts.startCommand)
	}
	if opts.staticDirSet && shouldWriteStaticDir(opts.runtime, opts.staticDir) {
		appendFlagValue("--static-dir", opts.staticDir)
	}
	if opts.appPortSet {
		appendFlagValue("--app-port", strconv.Itoa(opts.appPort))
	}
	if opts.healthcheckPathSet {
		appendFlagValue("--healthcheck-path", opts.healthcheckPath)
	}
	if opts.noHealthcheck {
		parts = append(parts, "--no-healthcheck")
	} else if opts.healthcheckSet {
		appendFlagValue("--healthcheck", opts.healthcheck)
	}
	if opts.noDeploy {
		parts = append(parts, "--no-deploy")
	}
	return strings.Join(parts, " ")
}
