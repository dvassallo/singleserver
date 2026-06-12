package singleserver

import (
	"flag"
	"strconv"
	"strings"
)

type appSettings struct {
	branch          string
	healthcheck     string
	healthcheckPath string
	runtime         string
	installCommand  string
	buildCommand    string
	startCommand    string
	staticDir       string
	deployTimeout   string
	appPort         int

	branchSet          bool
	healthcheckSet     bool
	healthcheckPathSet bool
	runtimeSet         bool
	installSet         bool
	buildSet           bool
	startSet           bool
	staticDirSet       bool
	deployTimeoutSet   bool
	appPortSet         bool
}

func bindAppSettingsFlags(fs *flag.FlagSet, settings *appSettings) *int {
	fs.StringVar(&settings.branch, "branch", "", "branch override")
	fs.StringVar(&settings.healthcheck, "healthcheck", "", "external healthcheck URL")
	fs.StringVar(&settings.healthcheckPath, "healthcheck-path", "", "container healthcheck path for generated Kamal config")
	fs.StringVar(&settings.runtime, "runtime", "", "generated Dockerfile runtime: static, node, or bun")
	fs.StringVar(&settings.installCommand, "install", "", "install command for generated Node/Bun Dockerfile")
	fs.StringVar(&settings.buildCommand, "build", "", "build command for generated Node/Bun Dockerfile")
	fs.StringVar(&settings.startCommand, "start", "", "start command for generated Node/Bun Dockerfile")
	fs.StringVar(&settings.staticDir, "static-dir", "", "static output directory for generated Dockerfile")
	fs.StringVar(&settings.deployTimeout, "deploy-timeout", "", "deploy timeout, a Go duration like 20m")
	return fs.Int("app-port", 0, "container app port for generated Kamal config")
}

func noteAppSettingsFlag(settings *appSettings, name string) {
	switch name {
	case "branch":
		settings.branchSet = true
	case "healthcheck":
		settings.healthcheckSet = true
	case "healthcheck-path":
		settings.healthcheckPathSet = true
	case "runtime":
		settings.runtimeSet = true
	case "install":
		settings.installSet = true
	case "build":
		settings.buildSet = true
	case "start":
		settings.startSet = true
	case "static-dir":
		settings.staticDirSet = true
	case "deploy-timeout":
		settings.deployTimeoutSet = true
	case "app-port":
		settings.appPortSet = true
	}
}

func appSettingsFlagTakesValue(arg string) bool {
	name := strings.TrimLeft(arg, "-")
	if before, _, ok := strings.Cut(name, "="); ok {
		name = before
	}
	switch name {
	case "branch", "healthcheck", "healthcheck-path", "runtime", "install", "build", "start", "static-dir", "deploy-timeout", "app-port":
		return true
	default:
		return false
	}
}

func appendAppSettingsFlags(parts []string, settings appSettings, onlySet bool) []string {
	appendFlagValue := func(name, value string) {
		if strings.TrimSpace(value) != "" {
			parts = append(parts, name, shellQuote(value))
		}
	}

	if !onlySet || settings.branchSet {
		appendFlagValue("--branch", settings.branch)
	}
	if !onlySet || settings.runtimeSet {
		appendFlagValue("--runtime", settings.runtime)
	}
	if !onlySet || settings.installSet {
		appendFlagValue("--install", settings.installCommand)
	}
	if !onlySet || settings.buildSet {
		appendFlagValue("--build", settings.buildCommand)
	}
	if !onlySet || settings.startSet {
		appendFlagValue("--start", settings.startCommand)
	}
	if (!onlySet || settings.staticDirSet) && shouldWriteStaticDir(settings.runtime, settings.staticDir) {
		appendFlagValue("--static-dir", settings.staticDir)
	}
	if settings.appPortSet {
		appendFlagValue("--app-port", strconv.Itoa(settings.appPort))
	}
	if !onlySet || settings.deployTimeoutSet {
		appendFlagValue("--deploy-timeout", settings.deployTimeout)
	}
	if settings.healthcheckPathSet {
		appendFlagValue("--healthcheck-path", settings.healthcheckPath)
	}
	if !onlySet || settings.healthcheckSet {
		appendFlagValue("--healthcheck", settings.healthcheck)
	}
	return parts
}

func applyAppSettings(app AppConfig, settings appSettings, dockerfile bool, noHealthcheck bool) (AppConfig, error) {
	if settings.branchSet {
		app.Branch = settings.branch
	}
	if dockerfile {
		clearGeneratedRuntime(&app)
		if !settings.healthcheckPathSet {
			app.HealthcheckPath = ""
		}
	}
	if settings.runtimeSet {
		clearGeneratedRuntime(&app)
		app.Runtime = settings.runtime
		if !settings.healthcheckPathSet {
			app.HealthcheckPath = ""
		}
	}
	if settings.installSet {
		app.InstallCommand = settings.installCommand
	}
	if settings.buildSet {
		app.BuildCommand = settings.buildCommand
	}
	if settings.startSet {
		app.StartCommand = settings.startCommand
	}
	if settings.staticDirSet {
		app.StaticDir = settings.staticDir
	}
	if settings.appPortSet {
		app.AppPort = settings.appPort
		app.AppPortSet = true
	}
	if app.Runtime != "" && (app.Runtime == "static" || app.StaticDir != "") && !settings.appPortSet {
		app.AppPort = 80
		app.AppPortSet = false
	}
	if settings.healthcheckPathSet {
		app.HealthcheckPath = settings.healthcheckPath
	}
	if settings.deployTimeoutSet {
		app.DeployTimeout = settings.deployTimeout
	}
	if noHealthcheck {
		app.Healthcheck = ""
	}
	if settings.healthcheckSet {
		app.Healthcheck = settings.healthcheck
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
