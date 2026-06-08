package singleserver

import (
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

type addOptions struct {
	repo               string
	name               string
	branch             string
	repoDir            string
	hosts              repeatedStrings
	healthcheck        string
	healthcheckPath    string
	appPort            int
	dryRun             bool
	noDeploy           bool
	healthcheckPathSet bool
	appPortSet         bool
}

type repeatedStrings []string

func (s *repeatedStrings) String() string {
	return strings.Join(*s, ",")
}

func (s *repeatedStrings) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("value cannot be empty")
	}
	*s = append(*s, value)
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
	appPort         int
	appPortSet      bool
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

	app, entry, err := opts.app()
	if err != nil {
		return err
	}
	if _, err := GeneratedDeployYAML(app); err != nil {
		return err
	}

	github := NewGitHubClient(envDefault("SINGLESERVER_STATE_DIR", "/etc/singleserver"))
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
	if !hasDockerfile {
		return fmt.Errorf("%s does not have a Dockerfile on %s", opts.repo, targetBranch)
	}

	body, err := readConfigForAppend(configPath)
	if err != nil {
		return err
	}
	updated, err := appendAppToConfigYAML(body, entry)
	if err != nil {
		return err
	}

	fmt.Fprintf(w, "%s\tgithub_installation\tok\tid=%d\n", app.Name, installationID)
	fmt.Fprintf(w, "%s\tdefault_branch\tok\t%s\n", app.Name, defaultBranch)
	fmt.Fprintf(w, "%s\tdockerfile\tok\tDockerfile on %s\n", app.Name, targetBranch)
	fmt.Fprintf(w, "%s\tdeploy_config\tok\tgenerated from conventions\n", app.Name)

	if opts.dryRun {
		fmt.Fprintf(w, "%s\tconfig\tdry_run\twould add to %s\n", app.Name, configPath)
		return nil
	}

	if err := writeFileAtomic(configPath, updated); err != nil {
		return err
	}
	fmt.Fprintf(w, "%s\tconfig\tok\tadded to %s\n", app.Name, configPath)

	if !opts.noDeploy {
		fmt.Fprintf(w, "%s\tdeploy\tstart\t%s\n", app.Name, targetBranch)
		if err := cliDeploy([]string{opts.repo, targetBranch}, logger); err != nil {
			return err
		}
		return cliDoctor(w)
	}

	fmt.Fprintf(w, "%s\tnext\tdeploy with `singleserver deploy %s` or push to %s\n", app.Name, opts.repo, targetBranch)
	return nil
}

func parseAddArgs(args []string, w io.Writer) (addOptions, error) {
	var opts addOptions
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	fs.SetOutput(w)
	fs.StringVar(&opts.name, "name", "", "app name override")
	fs.StringVar(&opts.branch, "branch", "", "branch override")
	fs.StringVar(&opts.repoDir, "path", "", "checkout path override")
	fs.Var(&opts.hosts, "host", "public host for generated Kamal proxy; can be repeated")
	fs.StringVar(&opts.healthcheck, "healthcheck", "", "external healthcheck URL")
	fs.StringVar(&opts.healthcheckPath, "healthcheck-path", "", "container healthcheck path for generated Kamal config")
	fs.BoolVar(&opts.dryRun, "dry-run", false, "validate without writing apps.yml")
	fs.BoolVar(&opts.noDeploy, "no-deploy", false, "configure without deploying immediately")

	appPort := fs.Int("app-port", 0, "container app port for generated Kamal config")
	if err := fs.Parse(normalizeAddArgs(args)); err != nil {
		return addOptions{}, err
	}
	opts.appPort = *appPort
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "healthcheck-path":
			opts.healthcheckPathSet = true
		case "app-port":
			opts.appPortSet = true
		}
	})

	if fs.NArg() != 1 {
		return addOptions{}, errors.New("usage: singleserver add <github-url> [--no-deploy]")
	}
	repo, err := normalizeRepoArg(fs.Arg(0))
	if err != nil {
		return addOptions{}, err
	}
	opts.repo = repo
	return opts, nil
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
		RepoDir:         o.repoDir,
		Hosts:           []string(o.hosts),
		Healthcheck:     o.healthcheck,
		HealthcheckPath: o.healthcheckPath,
	}
	if o.appPortSet {
		app.AppPort = o.appPort
	}
	if err := app.Normalize(); err != nil {
		return AppConfig{}, addAppEntry{}, err
	}
	if app.Healthcheck == "" && len(app.Hosts) > 0 {
		app.Healthcheck = "https://" + app.Hosts[0] + app.HealthcheckPath
	}

	entry := addAppEntry{
		repo:            app.Repo,
		hosts:           app.Hosts,
		healthcheck:     app.Healthcheck,
		healthcheckPath: "",
		appPort:         app.AppPort,
		appPortSet:      o.appPortSet,
	}
	if strings.TrimSpace(o.name) != "" {
		entry.name = app.Name
	}
	if strings.TrimSpace(o.branch) != "" {
		entry.branch = app.Branch
	}
	if strings.TrimSpace(o.repoDir) != "" {
		entry.repoDir = app.RepoDir
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
	if e.appPortSet {
		appendScalarPair(node, "app_port", strconv.Itoa(e.appPort))
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
		!e.appPortSet &&
		e.storage == nil
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
	case "name", "branch", "path", "host", "healthcheck", "healthcheck-path", "app-port":
		return true
	default:
		return false
	}
}
