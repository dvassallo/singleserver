package singleserver

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

var (
	addPromptInput           io.Reader = os.Stdin
	addPromptInteractiveFunc           = defaultAddPromptInteractive
)

type addOptions struct {
	appSettings
	repo           string
	name           string
	hosts          []string
	noDeploy       bool
	nonInteractive bool
	hostsSet       bool
}

const addUsage = "usage: singleserver add <github-url> [options]"

type addPromptContext struct {
	hasDockerfile bool
	targetBranch  string
}

type stringListFlag []string

func (f *stringListFlag) String() string {
	if f == nil {
		return ""
	}
	return strings.Join(*f, ",")
}

func (f *stringListFlag) Set(value string) error {
	value = strings.TrimSpace(value)
	if value != "" {
		*f = append(*f, value)
	}
	return nil
}

type addAppEntry struct {
	repo            string
	name            string
	branch          string
	repoDir         string
	hosts           []string
	healthcheck     string
	healthcheckPath string
	runtime         string
	installCommand  string
	buildCommand    string
	startCommand    string
	staticDir       string
	appPort         int
	appPortSet      bool
	deployTimeout   string
	storage         *StorageConfig
}

func cliAdd(args []string, w io.Writer, logger *log.Logger) error {
	opts, err := parseAddArgs(args, w)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	configPath := envDefault("SINGLESERVER_CONFIG", "/etc/singleserver/apps.yml")
	config, err := LoadConfig(configPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		config = &Config{}
	}
	if _, exists := config.AppByRepo(opts.repo); exists {
		return fmt.Errorf("%s is already configured", opts.repo)
	}

	github := NewGitHubClient(envDefault("SINGLESERVER_STATE_DIR", "/etc/singleserver"))
	if err := ensureGitHubSetupReady(github); err != nil {
		return err
	}
	installationID, err := github.RepositoryInstallationID(opts.repo)
	if err != nil {
		return err
	}
	token, err := github.DeployToken(installationID)
	if err != nil {
		return err
	}
	defaultBranch, err := github.RepositoryDefaultBranch(opts.repo, token)
	if err != nil {
		return err
	}
	targetBranch := opts.branch
	if targetBranch == "" {
		targetBranch = defaultBranch
	}
	hasDockerfile, err := github.RepositoryFileExists(opts.repo, "Dockerfile", targetBranch, token)
	if err != nil {
		return err
	}
	if hasDockerfile && strings.TrimSpace(opts.runtime) != "" {
		return fmt.Errorf("%s already has a Dockerfile on %s; remove --runtime or delete the repo Dockerfile", opts.repo, targetBranch)
	}

	if addPromptInteractiveFunc() && !opts.nonInteractive {
		opts, err = promptAddOptions(opts, addPromptInput, w, addPromptContext{
			hasDockerfile: hasDockerfile,
			targetBranch:  targetBranch,
		})
		if err != nil {
			return err
		}
	}

	app, entry, err := opts.app()
	if err != nil {
		return err
	}
	if existing, exists := config.AppByName(app.Name); exists {
		return fmt.Errorf("app name %s is already used by %s; rerun with --name <unique-name>", app.Name, existing.Repo)
	}
	if _, err := GeneratedDeployYAML(app); err != nil {
		return err
	}
	generatedDockerfile, err := GeneratedDockerfile(app)
	if err != nil {
		return err
	}

	dockerfileSource := fmt.Sprintf("Dockerfile on %s", targetBranch)
	if !hasDockerfile {
		if !app.UsesGeneratedDockerfile() {
			return fmt.Errorf("%s does not have a Dockerfile on %s; rerun with --runtime static|node|bun to generate one", opts.repo, targetBranch)
		}
		dockerfileSource = generatedDockerfile.Source
	}

	body, err := readConfigForAppend(configPath)
	if err != nil {
		return err
	}
	updated, err := appendAppToConfigYAML(body, entry)
	if err != nil {
		return err
	}

	writeCheck(w, app.Name, "github_installation", "ok", fmt.Sprintf("id=%d", installationID))
	writeCheck(w, app.Name, "default_branch", "ok", defaultBranch)
	writeCheck(w, app.Name, "dockerfile", "ok", dockerfileSource)
	writeCheck(w, app.Name, "deploy_config", "ok", "generated from conventions")

	syncedHosts := []string{}
	for _, host := range app.Hosts {
		if err := syncCloudflareAppDomainFunc(host, true, w); err != nil {
			for _, syncedHost := range syncedHosts {
				_ = syncCloudflareAppDomainFunc(syncedHost, false, io.Discard)
			}
			return err
		}
		syncedHosts = append(syncedHosts, host)
	}

	if err := writeFileAtomic(configPath, updated); err != nil {
		for _, syncedHost := range syncedHosts {
			_ = syncCloudflareAppDomainFunc(syncedHost, false, io.Discard)
		}
		return err
	}
	writeCheck(w, app.Name, "config", "ok", configPath, "added")

	if !opts.noDeploy {
		writeCheck(w, app.Name, "deploy", "start", targetBranch)
		if err := cliDeploy([]string{opts.repo, targetBranch}, w, logger); err != nil {
			return err
		}
		return cliDoctor(nil, w)
	}

	writeCheck(w, app.Name, "next", "pending", "deploy with `singleserver deploy "+opts.repo+"`", "or push to "+targetBranch)
	return nil
}

func ensureGitHubSetupReady(github *GitHubClient) error {
	if _, err := github.LoadSecrets(); err != nil {
		return errors.New("GitHub is not connected yet. Run `singleserver connect github`, open the setup URL, create/install the GitHub App, then rerun this command.")
	}
	if _, err := github.loadPrivateKey(); err != nil {
		return errors.New("GitHub App setup is incomplete. Run `singleserver connect github`, open the setup URL, create/install the GitHub App, then rerun this command.")
	}
	return nil
}

func parseAddArgs(args []string, w io.Writer) (addOptions, error) {
	var opts addOptions
	mode, args, err := commandModeFromArgs(args, addFlagTakesValue)
	if err != nil {
		return addOptions{}, err
	}
	opts.nonInteractive = mode.NonInteractive

	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	fs.SetOutput(w)
	fs.StringVar(&opts.name, "name", "", "app name override")
	fs.Var((*stringListFlag)(&opts.hosts), "domain", "app domain")
	fs.BoolVar(&opts.noDeploy, "no-deploy", false, "configure without deploying immediately")

	appPort := bindAppSettingsFlags(fs, &opts.appSettings)
	if err := fs.Parse(normalizeAddArgs(args)); err != nil {
		return addOptions{}, err
	}
	opts.appPort = *appPort
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "domain":
			opts.hostsSet = true
		default:
			noteAppSettingsFlag(&opts.appSettings, f.Name)
		}
	})

	if fs.NArg() != 1 {
		return addOptions{}, errors.New(addUsage)
	}
	repo, err := normalizeRepoArg(fs.Arg(0))
	if err != nil {
		return addOptions{}, err
	}
	opts.repo = repo
	return opts, nil
}

func defaultAddPromptInteractive() bool {
	stat, err := os.Stdin.Stat()
	return err == nil && stat.Mode()&os.ModeCharDevice != 0
}

type addPrompter struct {
	reader *bufio.Reader
	w      io.Writer
}

type flushWriter interface {
	Flush() error
}

func promptAddOptions(opts addOptions, input io.Reader, w io.Writer, ctx addPromptContext) (addOptions, error) {
	p := addPrompter{reader: bufio.NewReader(input), w: w}
	fmt.Fprintf(w, "Interactive setup for %s on %s.\n", opts.repo, ctx.targetBranch)
	if ctx.hasDockerfile {
		fmt.Fprintln(w, "Dockerfile found. Single Server will use it as-is.")
	} else {
		fmt.Fprintln(w, "No Dockerfile found. Single Server can generate one from explicit runtime settings.")
		runtime := strings.ToLower(strings.TrimSpace(opts.runtime))
		if runtime == "" {
			var err error
			runtime, err = p.askChoice("Runtime", []string{"static", "node", "bun"})
			if err != nil {
				return addOptions{}, err
			}
			opts.runtime = runtime
		} else {
			opts.runtime = runtime
		}
		if err := promptGeneratedDockerfileOptions(&opts, p); err != nil {
			return addOptions{}, err
		}
	}

	if len(opts.hosts) == 0 {
		value, err := p.askOptional("App domain (optional)")
		if err != nil {
			return addOptions{}, err
		}
		if value != "" {
			opts.hosts = []string{value}
			opts.hostsSet = true
		}
	}
	if !opts.healthcheckPathSet {
		defaultPath := promptHealthcheckPathDefault(opts.runtime, opts.staticDir)
		value, err := p.askDefault("Healthcheck path", defaultPath)
		if err != nil {
			return addOptions{}, err
		}
		if value != defaultPath {
			opts.healthcheckPath = value
			opts.healthcheckPathSet = true
		}
	}
	if strings.TrimSpace(opts.healthcheck) == "" {
		value, err := p.askOptional("External healthcheck URL (optional)")
		if err != nil {
			return addOptions{}, err
		}
		opts.healthcheck = value
	}
	if !opts.noDeploy {
		deploy, err := p.askYesNo("Deploy now?", true)
		if err != nil {
			return addOptions{}, err
		}
		if !deploy {
			opts.noDeploy = true
		}
	}

	fmt.Fprintf(w, "Equivalent command:\n  %s\n", addEquivalentCommand(opts))
	return opts, nil
}

func promptGeneratedDockerfileOptions(opts *addOptions, p addPrompter) error {
	switch strings.ToLower(strings.TrimSpace(opts.runtime)) {
	case "static":
		if strings.TrimSpace(opts.staticDir) == "" {
			value, err := p.askDefault("Static directory", ".")
			if err != nil {
				return err
			}
			opts.staticDir = value
		}
	case "node", "bun":
		if strings.TrimSpace(opts.installCommand) == "" {
			value, err := p.askOptional("Install command (optional)")
			if err != nil {
				return err
			}
			opts.installCommand = value
		}
		if strings.TrimSpace(opts.buildCommand) == "" {
			value, err := p.askOptional("Build command (optional)")
			if err != nil {
				return err
			}
			opts.buildCommand = value
		}
		if strings.TrimSpace(opts.staticDir) == "" && strings.TrimSpace(opts.startCommand) == "" {
			value, err := p.askOptional("Static output directory (blank for web process)")
			if err != nil {
				return err
			}
			opts.staticDir = value
		}
		if strings.TrimSpace(opts.staticDir) == "" {
			if strings.TrimSpace(opts.startCommand) == "" {
				value, err := p.askRequired("Start command")
				if err != nil {
					return err
				}
				opts.startCommand = value
			}
			if !opts.appPortSet {
				value, err := p.askPort("App port")
				if err != nil {
					return err
				}
				opts.appPort = value
				opts.appPortSet = true
			}
		}
	}
	return nil
}

func promptHealthcheckPathDefault(runtime, staticDir string) string {
	if strings.EqualFold(runtime, "static") || strings.TrimSpace(staticDir) != "" {
		return "/up"
	}
	return "/"
}

func (p addPrompter) askChoice(label string, values []string) (string, error) {
	allowed := map[string]bool{}
	for _, value := range values {
		allowed[value] = true
	}
	for {
		value, err := p.ask(label+" ("+strings.Join(values, "/")+")", "")
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

func (p addPrompter) askDefault(label, defaultValue string) (string, error) {
	value, err := p.ask(label, defaultValue)
	if err != nil {
		return "", err
	}
	if value == "" {
		return defaultValue, nil
	}
	return value, nil
}

func (p addPrompter) askOptional(label string) (string, error) {
	return p.ask(label, "")
}

func (p addPrompter) askRequired(label string) (string, error) {
	for {
		value, err := p.ask(label, "")
		if err != nil {
			return "", err
		}
		if value != "" {
			return value, nil
		}
		fmt.Fprintln(p.w, "This value is required.")
	}
}

func (p addPrompter) askPort(label string) (int, error) {
	for {
		value, err := p.askRequired(label)
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

func (p addPrompter) askYesNo(label string, defaultYes bool) (bool, error) {
	defaultValue := "y"
	if !defaultYes {
		defaultValue = "n"
	}
	for {
		value, err := p.askDefault(label, defaultValue)
		if err != nil {
			return false, err
		}
		switch strings.ToLower(value) {
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		default:
			fmt.Fprintln(p.w, "Enter y or n.")
		}
	}
}

func (p addPrompter) ask(label, defaultValue string) (string, error) {
	if defaultValue != "" {
		fmt.Fprintf(p.w, "%s [%s]: ", label, defaultValue)
	} else {
		fmt.Fprintf(p.w, "%s: ", label)
	}
	if flusher, ok := p.w.(flushWriter); ok {
		if err := flusher.Flush(); err != nil {
			return "", err
		}
	}
	line, err := p.reader.ReadString('\n')
	if err != nil && len(line) == 0 {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func addEquivalentCommand(opts addOptions) string {
	parts := []string{"singleserver", "add", shellQuote("https://github.com/" + opts.repo)}
	appendFlagValue := func(name, value string) {
		if strings.TrimSpace(value) != "" {
			parts = append(parts, name, shellQuote(value))
		}
	}

	appendFlagValue("--name", opts.name)
	for _, host := range opts.hosts {
		appendFlagValue("--domain", host)
	}
	parts = appendAppSettingsFlags(parts, opts.appSettings, false)
	if opts.noDeploy {
		parts = append(parts, "--no-deploy")
	}
	parts = append(parts, "--non-interactive")
	return strings.Join(parts, " ")
}

func normalizeRepoArg(value string) (string, error) {
	value = strings.TrimSpace(value)
	if repoPattern.MatchString(value) {
		return value, nil
	}

	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" {
		return value, nil
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return "", fmt.Errorf("repository URL must use https: %s", value)
	}
	if !strings.EqualFold(parsed.Host, "github.com") {
		return "", fmt.Errorf("repository URL must be on github.com: %s", value)
	}
	path := strings.Trim(parsed.Path, "/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 {
		return "", fmt.Errorf("repository URL must look like https://github.com/owner/repo: %s", value)
	}
	repoName := strings.TrimSuffix(parts[1], ".git")
	repo := parts[0] + "/" + repoName
	if !repoPattern.MatchString(repo) {
		return "", fmt.Errorf("invalid GitHub repository URL: %s", value)
	}
	return repo, nil
}

func (o addOptions) app() (AppConfig, addAppEntry, error) {
	app := AppConfig{
		Repo:            o.repo,
		Name:            o.name,
		Branch:          o.branch,
		Hosts:           o.hosts,
		Healthcheck:     o.healthcheck,
		HealthcheckPath: o.healthcheckPath,
		Runtime:         o.runtime,
		InstallCommand:  o.installCommand,
		BuildCommand:    o.buildCommand,
		StartCommand:    o.startCommand,
		StaticDir:       o.staticDir,
		DeployTimeout:   o.deployTimeout,
		AppPortSet:      o.appPortSet,
	}
	if o.appPortSet {
		app.AppPort = o.appPort
	}
	if err := app.Normalize(); err != nil {
		return AppConfig{}, addAppEntry{}, err
	}
	entry := addAppEntry{
		repo:            app.Repo,
		hosts:           app.Hosts,
		healthcheck:     app.Healthcheck,
		healthcheckPath: "",
		runtime:         app.Runtime,
		installCommand:  app.InstallCommand,
		buildCommand:    app.BuildCommand,
		startCommand:    app.StartCommand,
		staticDir:       app.StaticDir,
		appPort:         app.AppPort,
		appPortSet:      o.appPortSet,
		deployTimeout:   app.DeployTimeout,
	}
	if strings.TrimSpace(o.name) != "" {
		entry.name = app.Name
	}
	if strings.TrimSpace(o.branch) != "" {
		entry.branch = app.Branch
	}
	if o.healthcheckPathSet {
		entry.healthcheckPath = app.HealthcheckPath
	}
	return app, entry, nil
}

func readConfigForAppend(path string) ([]byte, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return body, nil
}

func appendAppToConfigYAML(body []byte, entry addAppEntry) ([]byte, error) {
	var doc yaml.Node
	if len(bytes.TrimSpace(body)) == 0 {
		doc.Kind = yaml.DocumentNode
		doc.Content = []*yaml.Node{{Kind: yaml.MappingNode}}
	} else if err := yaml.Unmarshal(body, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 {
		doc.Content = []*yaml.Node{{Kind: yaml.MappingNode}}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, errors.New("config root must be a mapping")
	}

	apps := findMapValue(root, "apps")
	if apps == nil {
		root.Content = append(root.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "apps"},
			&yaml.Node{Kind: yaml.SequenceNode},
		)
		apps = root.Content[len(root.Content)-1]
	}
	if apps.Kind != yaml.SequenceNode {
		return nil, errors.New("config apps must be a sequence")
	}
	apps.Content = append(apps.Content, entry.yamlNode())

	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(&doc); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}

	var config Config
	if err := yaml.Unmarshal(buf.Bytes(), &config); err != nil {
		return nil, err
	}
	if err := config.Normalize(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (e addAppEntry) yamlNode() *yaml.Node {
	if e.isScalar() {
		return &yaml.Node{Kind: yaml.ScalarNode, Value: e.repo}
	}

	node := &yaml.Node{Kind: yaml.MappingNode}
	appendScalarPair(node, "repo", e.repo)
	if e.name != "" {
		appendScalarPair(node, "name", e.name)
	}
	if e.branch != "" {
		appendScalarPair(node, "branch", e.branch)
	}
	if e.repoDir != "" {
		appendScalarPair(node, "path", e.repoDir)
	}
	if len(e.hosts) > 0 {
		hostNode := &yaml.Node{Kind: yaml.SequenceNode}
		for _, host := range e.hosts {
			hostNode.Content = append(hostNode.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: host})
		}
		appendNodePair(node, "hosts", hostNode)
	}
	if e.healthcheck != "" {
		appendScalarPair(node, "healthcheck", e.healthcheck)
	}
	if e.healthcheckPath != "" {
		appendScalarPair(node, "healthcheck_path", e.healthcheckPath)
	}
	if e.runtime != "" {
		appendScalarPair(node, "runtime", e.runtime)
	}
	if e.installCommand != "" {
		appendScalarPair(node, "install", e.installCommand)
	}
	if e.buildCommand != "" {
		appendScalarPair(node, "build", e.buildCommand)
	}
	if e.startCommand != "" {
		appendScalarPair(node, "start", e.startCommand)
	}
	if shouldWriteStaticDir(e.runtime, e.staticDir) {
		appendScalarPair(node, "static_dir", e.staticDir)
	}
	if e.appPortSet {
		appendScalarPair(node, "app_port", strconv.Itoa(e.appPort))
	}
	if e.deployTimeout != "" {
		appendScalarPair(node, "deploy_timeout", e.deployTimeout)
	}
	if e.storage != nil {
		storageNode := &yaml.Node{Kind: yaml.MappingNode}
		if e.storage.Path != "" {
			appendScalarPair(storageNode, "path", e.storage.Path)
		}
		if e.storage.Mount != "" {
			appendScalarPair(storageNode, "mount", e.storage.Mount)
		}
		appendNodePair(node, "storage", storageNode)
	}
	return node
}

func (e addAppEntry) isScalar() bool {
	return e.name == "" &&
		e.branch == "" &&
		e.repoDir == "" &&
		len(e.hosts) == 0 &&
		e.healthcheck == "" &&
		e.healthcheckPath == "" &&
		e.runtime == "" &&
		e.installCommand == "" &&
		e.buildCommand == "" &&
		e.startCommand == "" &&
		!shouldWriteStaticDir(e.runtime, e.staticDir) &&
		!e.appPortSet &&
		e.deployTimeout == "" &&
		e.storage == nil
}

func shouldWriteStaticDir(runtime, staticDir string) bool {
	return staticDir != "" && !(runtime == "static" && staticDir == ".")
}

func findMapValue(node *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func appendScalarPair(node *yaml.Node, key string, value string) {
	appendNodePair(node, key, &yaml.Node{Kind: yaml.ScalarNode, Value: value})
}

func appendNodePair(node *yaml.Node, key string, value *yaml.Node) {
	node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, value)
}

func writeFileAtomic(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	mode := os.FileMode(0600)
	if stat, err := os.Stat(path); err == nil {
		mode = stat.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func normalizeAddArgs(args []string) []string {
	return normalizeFlagArgs(args, addFlagTakesValue)
}

func addFlagTakesValue(arg string) bool {
	name := strings.TrimLeft(arg, "-")
	if before, _, ok := strings.Cut(name, "="); ok {
		name = before
	}
	switch name {
	case "name", "domain":
		return true
	default:
		return appSettingsFlagTakesValue(arg)
	}
}
