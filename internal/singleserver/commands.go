package singleserver

import (
	"errors"
	"io"
	"log"
)

// argSpec documents a positional argument for help output.
type argSpec struct {
	Name string
	Desc string
}

// flagSpec documents a flag for help output. Defaults are folded into Desc so
// help stays a simple two-column list.
type flagSpec struct {
	Name string
	Desc string
}

// command is a node in the CLI command tree. It is the single source of truth
// for both dispatch and help: top-level help lists commands by Group, and each
// command renders its own detailed help from Usage, Args, Flags, and Children.
//
// Parent commands (Children set) route their subcommands inside Run; the child
// nodes carry help metadata only and need no Run of their own.
type command struct {
	Name     string
	Group    string
	Summary  string
	Usage    string // synopsis tail after the command path, e.g. "<app> [options]"
	Long     string
	Args     []argSpec
	Flags    []flagSpec
	Children []*command
	Run      func(args []string, w io.Writer, logger *log.Logger) error
}

// commandGroups is the display order for the top-level help, matching the docs
// command index.
var commandGroups = []string{"Setup", "Apps", "Monitoring", "Resources"}

var appSettingsFlagHelp = []flagSpec{
	{"--branch <name>", "Branch to deploy (default the repo default)"},
	{"--healthcheck <url>", "External URL to verify after each deploy"},
	{"--healthcheck-path <path>", "Container healthcheck path for the generated Kamal config"},
	{"--runtime <name>", "Generated Dockerfile runtime: static, node, or bun"},
	{"--install <cmd>", "Install command for the generated Node/Bun Dockerfile"},
	{"--build <cmd>", "Build command for the generated Node/Bun Dockerfile"},
	{"--start <cmd>", "Start command for the generated Node/Bun Dockerfile"},
	{"--static-dir <dir>", "Static output directory for the generated Dockerfile"},
	{"--app-port <port>", "Container app port for the generated Kamal config"},
	{"--deploy-timeout <dur>", "Deploy timeout as a Go duration like 20m"},
}

// cliCommands is the registry. Adding a command here wires up both its dispatch
// and its help in one place.
var cliCommands = []*command{
	{
		Name:    "setup",
		Group:   "Setup",
		Summary: "Guided first-run setup wizard",
		Long:    "Walk through connecting Tailscale, Cloudflare, and GitHub. Prompts only where input is needed and is safe to rerun; it repairs whatever is not connected yet.",
		Run: func(args []string, w io.Writer, logger *log.Logger) error {
			return cliSetup(args, w)
		},
	},
	{
		Name:    "connect",
		Group:   "Setup",
		Summary: "Connect or repair a provider",
		Usage:   "<tailscale|cloudflare|github> [options]",
		Long:    "Connect or repair the Tailscale, Cloudflare, or GitHub integration. It is safe to rerun and repairs whatever is missing.",
		Children: []*command{
			{
				Name:    "tailscale",
				Summary: "Join the Tailscale network",
				Usage:   "[options]",
				Flags: []flagSpec{
					{"--auth-key <key>", "Auth key for an unattended join"},
					{"--hostname <name>", "Hostname to register on the tailnet"},
				},
			},
			{
				Name:    "cloudflare",
				Summary: "Set up the Cloudflare tunnel and DNS",
				Usage:   "[options]",
				Flags: []flagSpec{
					{"--account <id>", "Cloudflare account id (chosen automatically when you have one)"},
					{"--tunnel <name>", "Tunnel name (default singleserver-<host>)"},
				},
			},
			{
				Name:    "github",
				Summary: "Print the GitHub App setup URL",
				Long:    "Ensures the base files exist and prints the URL to create and install the GitHub App, then restarts the service.",
			},
		},
		Run: func(args []string, w io.Writer, logger *log.Logger) error {
			if len(args) >= 1 {
				switch args[0] {
				case "tailscale":
					return cliTailscaleConnect(args[1:], w)
				case "cloudflare":
					return cliCloudflareConnect(args[1:], w)
				case "github":
					return cliGitHubConnect(args[1:], w)
				}
			}
			return errors.New("usage: singleserver connect <tailscale|cloudflare|github> [options]")
		},
	},
	{
		Name:    "upgrade",
		Group:   "Setup",
		Summary: "Re-run the installer and restart Single Server",
		Usage:   "[--edge]",
		Long:    "Re-runs the installer to fetch the latest release and restarts the service. Use --edge to track the latest build of main instead of the latest tagged release.",
		Flags: []flagSpec{
			{"--edge", "Install the latest edge build from main instead of the latest release"},
		},
		Run: func(args []string, w io.Writer, logger *log.Logger) error {
			return cliUpgrade(args, w)
		},
	},
	{
		Name:    "version",
		Group:   "Setup",
		Summary: "Print the installed version",
		Long:    "Print the version, commit, and build date of the installed binary.",
		Run: func(args []string, w io.Writer, logger *log.Logger) error {
			return printVersion(w)
		},
	},
	{
		Name:    "add",
		Group:   "Apps",
		Summary: "Add and deploy a repository",
		Usage:   "<github-url> [options]",
		Long:    "Configure a GitHub repository as an app and deploy it. When the repo has no Kamal config, Single Server detects the runtime and generates one along with a Dockerfile.",
		Args:    []argSpec{{"<github-url>", "HTTPS URL or owner/repo of the GitHub repository"}},
		Flags: append([]flagSpec{
			{"--name <name>", "App name override (default derived from the repo)"},
			{"--domain <host>", "Public domain to route to the app; repeat for several"},
			{"--env <KEY=value>", "Environment variable stored on the server and injected at deploy; repeat for several"},
		}, append(appSettingsFlagHelp, flagSpec{"--no-deploy", "Configure without deploying immediately"})...),
		Run: func(args []string, w io.Writer, logger *log.Logger) error {
			return cliAdd(args, w, logger)
		},
	},
	{
		Name:    "edit",
		Group:   "Apps",
		Summary: "Edit app config",
		Usage:   "<app> [options]",
		Long:    "Edit a configured app. With no flags it walks through the settings interactively. Redeploys unless --no-deploy.",
		Args:    []argSpec{{"<app>", "App name, owner/repo, or GitHub URL"}},
		Flags: append([]flagSpec{
			{"--no-healthcheck", "Clear the external healthcheck URL"},
			{"--dockerfile", "Use the repository Dockerfile and clear generated runtime settings"},
		}, append(appSettingsFlagHelp, flagSpec{"--no-deploy", "Update config without deploying"})...),
		Run: func(args []string, w io.Writer, logger *log.Logger) error {
			return cliEdit(args, w, logger)
		},
	},
	{
		Name:    "deploy",
		Group:   "Apps",
		Summary: "Deploy a configured app",
		Usage:   "[app] [ref]",
		Long:    "Deploy a configured app at a given ref. Fetches the commit through the GitHub App and runs the deploy pipeline.",
		Args: []argSpec{
			{"[app]", "App to deploy; prompts when omitted in an interactive session"},
			{"[ref]", "Git branch, tag, or SHA to deploy (default the app branch)"},
		},
		Run: func(args []string, w io.Writer, logger *log.Logger) error {
			return cliDeploy(args, w, logger)
		},
	},
	{
		Name:    "remove",
		Group:   "Apps",
		Summary: "Remove an app, optionally its repo and storage",
		Usage:   "<app> [options]",
		Long:    "Remove an app from config and stop its containers. Keeps the repository checkout and storage unless told to delete them.",
		Args:    []argSpec{{"<app>", "App to remove"}},
		Flags: []flagSpec{
			{"--delete-storage", "Also delete the app's persistent storage"},
			{"--delete-repo", "Also delete the repository checkout"},
		},
		Run: func(args []string, w io.Writer, logger *log.Logger) error {
			return cliRemove(args, w)
		},
	},
	{
		Name:    "list",
		Group:   "Monitoring",
		Summary: "Show configured apps",
		Long:    "List configured apps with their domain, repo, and current state.",
		Run: func(args []string, w io.Writer, logger *log.Logger) error {
			return cliList(w)
		},
	},
	{
		Name:    "status",
		Group:   "Monitoring",
		Summary: "Show daemon and app health",
		Long:    "Show the daemon state and a per-app summary of runtime, last deploy, and healthcheck.",
		Run: func(args []string, w io.Writer, logger *log.Logger) error {
			return cliStatus(w)
		},
	},
	{
		Name:    "logs",
		Group:   "Monitoring",
		Summary: "Show recent deploy or runtime logs",
		Usage:   "[app] [options]",
		Long:    "Show recent deploy logs from the daemon journal, or the app container logs with --runtime.",
		Args:    []argSpec{{"[app]", "Limit logs to one app"}},
		Flags: []flagSpec{
			{"--follow", "Stream new log lines as they arrive"},
			{"--runtime", "Show the app container logs instead of deploy logs"},
			{"--daemon", "Show the full daemon journal, unfiltered"},
		},
		Run: func(args []string, w io.Writer, logger *log.Logger) error {
			return cliLogs(args, w)
		},
	},
	{
		Name:    "doctor",
		Group:   "Monitoring",
		Summary: "Run full diagnostic checks",
		Usage:   "[app]",
		Long:    "Check config, deploy plumbing, GitHub App access, checkouts, recent deploys, and healthchecks.",
		Args:    []argSpec{{"[app]", "Limit checks to one app"}},
		Run: func(args []string, w io.Writer, logger *log.Logger) error {
			return cliDoctor(args, w)
		},
	},
	{
		Name:    "inspect",
		Group:   "Monitoring",
		Summary: "Print the generated Kamal config",
		Usage:   "<app>",
		Long:    "Print the deploy.yml that Single Server generates for an app.",
		Args:    []argSpec{{"<app>", "App name, owner/repo, or GitHub URL"}},
		Run: func(args []string, w io.Writer, logger *log.Logger) error {
			return cliInspect(args, w)
		},
	},
	{
		Name:    "domains",
		Group:   "Resources",
		Summary: "Manage app domains",
		Usage:   "<add|remove|list|verify> ...",
		Long:    "Add, remove, list, or verify the domains routed to your apps.",
		Children: []*command{
			{
				Name:    "add",
				Summary: "Route a domain to an app",
				Usage:   "<app> <domain> [--no-deploy]",
				Args:    []argSpec{{"<app>", "App to route to"}, {"<domain>", "Hostname to route, like app.example.com"}},
				Flags:   []flagSpec{{"--no-deploy", "Update config and DNS without deploying"}},
			},
			{
				Name:    "remove",
				Summary: "Stop routing a domain",
				Usage:   "<app> <domain> [--no-deploy]",
				Args:    []argSpec{{"<app>", "App to update"}, {"<domain>", "Hostname to stop routing"}},
				Flags:   []flagSpec{{"--no-deploy", "Update config and DNS without deploying"}},
			},
			{
				Name:    "list",
				Summary: "List configured domains",
				Usage:   "[app]",
				Args:    []argSpec{{"[app]", "Limit to one app"}},
			},
			{
				Name:    "verify",
				Summary: "Check DNS and routing for domains",
				Usage:   "[app]",
				Args:    []argSpec{{"[app]", "Limit to one app"}},
			},
		},
		Run: func(args []string, w io.Writer, logger *log.Logger) error {
			return cliDomains(args, w, logger)
		},
	},
	{
		Name:    "env",
		Group:   "Resources",
		Summary: "Manage app env vars",
		Usage:   "<set|list|unset> ...",
		Long:    "Manage server-side environment variables for an app. Values live in a per-app env file on the server, never in the repo.",
		Children: []*command{
			{
				Name:    "set",
				Summary: "Set an environment variable",
				Usage:   "<app> KEY=value",
				Args:    []argSpec{{"<app>", "App to update"}, {"KEY=value", "Variable name and value"}},
			},
			{
				Name:    "list",
				Summary: "List environment variables",
				Usage:   "<app>",
				Args:    []argSpec{{"<app>", "App to read"}},
			},
			{
				Name:    "unset",
				Summary: "Remove an environment variable",
				Usage:   "<app> KEY",
				Args:    []argSpec{{"<app>", "App to update"}, {"KEY", "Variable name to remove"}},
			},
		},
		Run: func(args []string, w io.Writer, logger *log.Logger) error {
			return cliEnv(args, w)
		},
	},
	{
		Name:    "storage",
		Group:   "Resources",
		Summary: "Enable or disable persistent storage",
		Usage:   "<enable|disable> <app> [options]",
		Long:    "Give an app a host directory that survives deploys, mounted into the container.",
		Children: []*command{
			{
				Name:    "enable",
				Summary: "Create and mount persistent storage",
				Usage:   "<app> [options]",
				Args:    []argSpec{{"<app>", "App to give storage"}},
				Flags: []flagSpec{
					{"--mount <path>", "Mount path inside the container (default /storage)"},
					{"--path <path>", "Host directory (default /srv/storage/<app>)"},
					{"--no-deploy", "Stage the config without deploying"},
				},
			},
			{
				Name:    "disable",
				Summary: "Detach storage and redeploy",
				Usage:   "<app> [options]",
				Long:    "Clear the storage config and redeploy without the mount. Keeps the host directory unless --delete is given.",
				Args:    []argSpec{{"<app>", "App to detach storage from"}},
				Flags: []flagSpec{
					{"--delete", "Also delete the host directory and its data"},
					{"--no-deploy", "Stage the config without deploying"},
				},
			},
		},
		Run: func(args []string, w io.Writer, logger *log.Logger) error {
			return cliStorage(args, w, logger)
		},
	},
	{
		Name:    "backup",
		Group:   "Resources",
		Summary: "Back up app storage",
		Usage:   "<app>",
		Long:    "Snapshot an app's storage into a timestamped archive under the backups directory.",
		Args:    []argSpec{{"<app>", "App whose storage to snapshot"}},
		Run: func(args []string, w io.Writer, logger *log.Logger) error {
			return cliBackup(args, w)
		},
	},
	{
		Name:    "restore",
		Group:   "Resources",
		Summary: "Restore app storage",
		Usage:   "<app> <backup> [--no-restart]",
		Long:    "Restore an app's storage from a backup, keeping the previous directory as a safety copy.",
		Args: []argSpec{
			{"<app>", "App to restore"},
			{"<backup>", "Backup id or path to restore from"},
		},
		Flags: []flagSpec{{"--no-restart", "Restore files without restarting the app containers"}},
		Run: func(args []string, w io.Writer, logger *log.Logger) error {
			return cliRestore(args, w)
		},
	},
}

func lookupCommand(name string) *command {
	for _, c := range cliCommands {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func (c *command) child(name string) *command {
	for _, ch := range c.Children {
		if ch.Name == name {
			return ch
		}
	}
	return nil
}
