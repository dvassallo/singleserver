package singleserver

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// tailscaleUpInteractiveFunc runs the browser login. It is a seam so tests can
// stand in for the real `tailscale up`, which blocks on a human clicking a URL.
var tailscaleUpInteractiveFunc = runTailscaleUpInteractive

func runTailscaleUpInteractive() error {
	cmd := exec.Command("tailscale", "up", "--ssh")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// cliSetup is the guided first-run wizard. It connects Tailscale, Cloudflare,
// and GitHub in turn, prompts only when a step needs input, and finishes with a
// short summary instead of a full diagnostic dump. It is safe to rerun: each
// step detects what is already connected and repairs the rest. Steps never abort
// the wizard, so an unattended or partial run still prints how to finish.
func cliSetup(args []string, w io.Writer) error {
	mode, args, err := commandModeFromArgs(args, noFlagValues)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(w)
	if err := fs.Parse(normalizeFlagArgs(args, noFlagValues)); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: singleserver setup")
	}
	if err := ensureBaseFiles(); err != nil {
		return err
	}

	interactive := cliCanPrompt(mode)
	p := interactivePrompter(w)

	fmt.Fprintln(w, bold("Single Server setup")+dim(" · 3 steps"))

	setupStepHeader(w, 1, "Tailscale", "joins this server to your tailnet and gives GitHub a public URL")
	reportSetupStep(w, setupTailscale(w, p, interactive))

	setupStepHeader(w, 2, "Cloudflare", "routes your domains through a tunnel with TLS")
	reportSetupStep(w, setupCloudflare(w, p, interactive))

	setupStepHeader(w, 3, "GitHub", "lets every git push deploy automatically")
	reportSetupStep(w, setupGitHub(w, p, interactive))

	setupSummary(w)
	return nil
}

func setupStepHeader(w io.Writer, n int, title, why string) {
	flushSetupChecks(w)
	fmt.Fprintln(w)
	fmt.Fprintf(w, "%s  %s %s\n", dim(fmt.Sprintf("%d/3", n)), bold(title), dim("— "+why))
}

// reportSetupStep surfaces a step's hard error as a note without aborting the
// wizard, so later steps and the summary still run.
func reportSetupStep(w io.Writer, err error) {
	if err != nil {
		fmt.Fprintln(w, dim("  note: ")+err.Error())
	}
}

// flushSetupChecks renders any buffered checks immediately so they sit under the
// step header that produced them. It clears the started flag first so the wizard
// owns the blank lines between steps rather than the check renderer.
func flushSetupChecks(w io.Writer) {
	if o, ok := w.(*Output); ok {
		o.started = false
		o.flushChecks()
	}
}

func setupTailscale(w io.Writer, p addPrompter, interactive bool) error {
	env, _ := loadServiceEnv()
	if hasTailscalePublicURL(env) || tailscaleAlreadyRunning() || defaultTailscaleAuthKey() != "" {
		return cliTailscaleConnect(nil, w)
	}
	if !interactive {
		writeCheck(w, "tailscale", "login", "pending", "run `tailscale up --ssh`, then `singleserver connect tailscale`")
		return nil
	}
	yes, err := p.askYesNo("Connect Tailscale now?", true)
	if err != nil {
		return err
	}
	if !yes {
		writeCheck(w, "tailscale", "login", "skipped", "run `singleserver connect tailscale` when ready")
		return nil
	}
	if err := tailscaleUpInteractiveFunc(); err != nil {
		writeCheck(w, "tailscale", "login", "pending", "run `tailscale up --ssh`, then `singleserver connect tailscale`")
		return nil
	}
	return cliTailscaleConnect(nil, w)
}

func setupCloudflare(w io.Writer, p addPrompter, interactive bool) error {
	state, _ := loadCloudflareState()
	if cloudflareTokenFromEnvOrState(state) != "" {
		return cliCloudflareConnect(nil, w)
	}
	if !interactive {
		writeCheck(w, "cloudflare", "token", "pending", "set CLOUDFLARE_API_TOKEN, then `singleserver connect cloudflare`")
		return nil
	}
	yes, err := p.askYesNo("Connect Cloudflare now?", true)
	if err != nil {
		return err
	}
	if !yes {
		writeCheck(w, "cloudflare", "token", "skipped", "run `singleserver connect cloudflare` when ready")
		return nil
	}
	// The token is echoed on purpose: over SSH a hidden paste gives no feedback
	// on whether it landed, and seeing it is more reliable than guessing.
	token, err := p.askOptional("Cloudflare API token")
	if err != nil {
		return err
	}
	token = strings.TrimSpace(token)
	if token == "" {
		writeCheck(w, "cloudflare", "token", "pending", "set CLOUDFLARE_API_TOKEN, then `singleserver connect cloudflare`")
		return nil
	}
	if err := os.Setenv("CLOUDFLARE_API_TOKEN", token); err != nil {
		return err
	}
	return cliCloudflareConnect(nil, w)
}

func setupGitHub(w io.Writer, p addPrompter, interactive bool) error {
	if githubAppInstalled() {
		writeCheck(w, "github", "app", "ok", "already installed")
		return nil
	}
	env, _ := loadServiceEnv()
	if publicURLValue(env) == "" {
		writeCheck(w, "github", "app", "pending", "connect Tailscale first, then `singleserver connect github`")
		return nil
	}
	if err := cliGitHubConnect(nil, w); err != nil {
		return err
	}
	if !interactive {
		return nil
	}
	flushSetupChecks(w)
	fmt.Fprint(w, "After you install the GitHub App in your browser, press Enter to continue. ")
	if f, ok := w.(flushWriter); ok {
		_ = f.Flush()
	}
	if _, err := p.reader.ReadString('\n'); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if githubAppInstalled() {
		writeCheck(w, "github", "app", "ok", "installed")
	} else {
		writeCheck(w, "github", "app", "pending", "finish at the URL above, then `singleserver connect github`")
	}
	return nil
}

func setupSummary(w io.Writer) {
	flushSetupChecks(w)
	env, _ := loadServiceEnv()
	ts := hasTailscalePublicURL(env)
	cf := cloudflareConfigured()
	gh := githubAppInstalled()

	fmt.Fprintln(w)
	if ts && cf && gh {
		fmt.Fprintf(w, "%s %s%s\n", mark(stateOK), bold("Setup complete"), dim(" — Tailscale, Cloudflare, and GitHub connected"))
		fmt.Fprintln(w, dim("Deploy your first app:"))
		fmt.Fprintln(w, "  singleserver add https://github.com/you/your-app")
		return
	}
	fmt.Fprintf(w, "%s %s\n", mark(stateWarn), bold("Setup is not finished yet"))
	if !ts {
		fmt.Fprintln(w, "  Tailscale   "+dim("singleserver connect tailscale"))
	}
	if !cf {
		fmt.Fprintln(w, "  Cloudflare  "+dim("singleserver connect cloudflare"))
	}
	if !gh {
		fmt.Fprintln(w, "  GitHub      "+dim("singleserver connect github"))
	}
	fmt.Fprintln(w, dim("Rerun anytime with: singleserver setup"))
}

func publicURLValue(env map[string]string) string {
	return strings.TrimSpace(env["SINGLESERVER_PUBLIC_URL"])
}

func hasTailscalePublicURL(env map[string]string) bool {
	url := publicURLValue(env)
	return strings.HasPrefix(url, "https://") && strings.Contains(url, ".ts.net")
}

func tailscaleAlreadyRunning() bool {
	status, err := currentTailscaleStatus()
	return err == nil && tailscaleRunning(status)
}

func cloudflareConfigured() bool {
	state, err := loadCloudflareState()
	return err == nil && state != nil && strings.TrimSpace(state.TunnelID) != ""
}

func githubAppInstalled() bool {
	dir := envDefault("SINGLESERVER_STATE_DIR", "/etc/singleserver")
	if _, err := os.Stat(filepath.Join(dir, "github.json")); err != nil {
		return false
	}
	if _, err := os.Stat(filepath.Join(dir, "github.private-key.pem")); err != nil {
		return false
	}
	return true
}

func cliGitHubConnect(args []string, w io.Writer) error {
	_, args, err := commandModeFromArgs(args, githubFlagTakesValue)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("connect github", flag.ContinueOnError)
	fs.SetOutput(w)
	if err := fs.Parse(normalizeFlagArgs(args, githubFlagTakesValue)); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: singleserver connect github")
	}
	if err := ensureBaseFiles(); err != nil {
		return err
	}
	env, err := loadServiceEnv()
	if err != nil {
		return err
	}
	publicURL := strings.TrimRight(env["SINGLESERVER_PUBLIC_URL"], "/")
	if publicURL == "" {
		publicURL = "http://127.0.0.1:" + envDefault("SINGLESERVER_PORT", "8787")
	}
	token := env["SINGLESERVER_SETUP_TOKEN"]
	if token == "" {
		token, err = randomHex(24)
		if err != nil {
			return err
		}
		env["SINGLESERVER_SETUP_TOKEN"] = token
		if err := writeServiceEnv(env); err != nil {
			return err
		}
	}
	if err := commandRunFunc(10*time.Second, "systemctl", "restart", "singleserver.service"); err != nil {
		return err
	}
	writeCheck(w, "github", "connect", "ok", publicURL+"/setup/github-app?token="+token)
	fmt.Fprintln(w, "Open the setup URL, create/install the GitHub App, then rerun your command.")
	return nil
}

func cliCloudflareConnect(args []string, w io.Writer) error {
	mode, args, err := commandModeFromArgs(args, cloudflareFlagTakesValue)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("connect cloudflare", flag.ContinueOnError)
	fs.SetOutput(w)
	accountID := fs.String("account", "", "Cloudflare account id")
	tunnelName := fs.String("tunnel", "", "Cloudflare tunnel name")
	if err := fs.Parse(normalizeFlagArgs(args, cloudflareFlagTakesValue)); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: singleserver connect cloudflare [--account <id>] [--tunnel <name>]")
	}
	if err := ensureBaseFiles(); err != nil {
		return err
	}
	tunnelNameSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "tunnel" {
			tunnelNameSet = true
		}
	})

	state, err := loadCloudflareState()
	if err != nil {
		return err
	}
	token := cloudflareTokenFromEnvOrState(state)
	if token == "" && cliCanPrompt(mode) {
		value, err := interactivePrompter(w).askRequired("Cloudflare API token")
		if err != nil {
			return err
		}
		token = value
	}
	client, err := newCloudflareClient(token)
	if err != nil {
		return err
	}

	selectedAccountID, err := selectCloudflareAccount(client, *accountID, cliCanPrompt(mode), w)
	if err != nil {
		return err
	}
	state.APIToken = token
	state.AccountID = selectedAccountID
	applyCloudflareTunnelName(state, *tunnelName, tunnelNameSet)
	state.HookHost = ""
	if state.CredentialsFile == "" {
		state.CredentialsFile = "/etc/cloudflared/singleserver.json"
	}
	if state.ConfigFile == "" {
		state.ConfigFile = "/etc/cloudflared/singleserver.yml"
	}

	if state.TunnelID == "" {
		tunnel, err := client.findTunnel(state.AccountID, state.TunnelName)
		if err != nil {
			return err
		}
		if tunnel != nil {
			state.TunnelID = tunnel.ID
			if state.TunnelSecret == "" {
				state.TunnelSecret = tunnel.Secret
			}
			if state.TunnelSecret == "" {
				return fmt.Errorf("Cloudflare tunnel %s already exists but its tunnel secret is unavailable; rerun with --tunnel <new-name> or copy the original /etc/singleserver/cloudflare.json", state.TunnelName)
			}
			writeCheck(w, "cloudflare", "tunnel", "ok", state.TunnelID, "reused "+state.TunnelName)
		} else {
			if state.TunnelSecret == "" {
				state.TunnelSecret, err = randomTunnelSecret()
				if err != nil {
					return err
				}
			}
			tunnel, err := client.createTunnel(state.AccountID, state.TunnelName, state.TunnelSecret)
			if err != nil {
				return err
			}
			state.TunnelID = tunnel.ID
			writeCheck(w, "cloudflare", "tunnel", "ok", state.TunnelID, "created "+state.TunnelName)
		}
	} else {
		writeCheck(w, "cloudflare", "tunnel", "ok", state.TunnelID)
	}

	if err := writeCloudflaredCredentials(state.CredentialsFile, state); err != nil {
		return err
	}
	if err := ensureCloudflaredConfig(state.ConfigFile, state.TunnelID, state.CredentialsFile); err != nil {
		return err
	}
	if err := writeCloudflareState(state); err != nil {
		return err
	}
	if err := pruneStaleCloudflareRoutes(client, state, w); err != nil {
		return err
	}
	if err := writeCloudflaredService(state.ConfigFile); err != nil {
		return err
	}
	if err := commandRunFunc(10*time.Second, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	if err := commandRunFunc(10*time.Second, "systemctl", "enable", "--now", "cloudflared-singleserver.service"); err != nil {
		return err
	}
	writeCheck(w, "cloudflare", "ingress", "ok", "apps", "target="+state.TunnelID+".cfargotunnel.com")
	return nil
}

func applyCloudflareTunnelName(state *CloudflareState, requestedName string, requestedExplicitly bool) {
	name := strings.TrimSpace(requestedName)
	if name == "" {
		name = defaultCloudflareTunnelName()
	}
	existingName := strings.TrimSpace(state.TunnelName)
	switch {
	case existingName != "" && !strings.EqualFold(existingName, name):
		state.TunnelID = ""
		state.TunnelSecret = ""
	case existingName == "" && requestedExplicitly && state.TunnelID != "":
		state.TunnelID = ""
		state.TunnelSecret = ""
	}
	state.TunnelName = name
}

func defaultCloudflareTunnelName() string {
	hostname, _ := os.Hostname()
	return cloudflareTunnelNameFromHostname(hostname)
}

func cloudflareTunnelNameFromHostname(hostname string) string {
	label := dnsLabelFromAppName(hostname)
	if label == "" {
		return "singleserver"
	}
	return "singleserver-" + label
}

func ensureBaseFiles() error {
	stateDir := envDefault("SINGLESERVER_STATE_DIR", "/etc/singleserver")
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		return err
	}
	if err := os.MkdirAll(reposRoot(), 0755); err != nil {
		return err
	}
	if err := os.MkdirAll(storageRoot(), 0755); err != nil {
		return err
	}
	if err := os.MkdirAll(backupRoot(), 0755); err != nil {
		return err
	}
	configPath := envDefault("SINGLESERVER_CONFIG", "/etc/singleserver/apps.yml")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		if err := writeFileAtomic(configPath, []byte("apps: []\n")); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	env, err := loadServiceEnv()
	if err != nil {
		return err
	}
	defaults := map[string]string{
		"SINGLESERVER_CONFIG":    configPath,
		"SINGLESERVER_STATE_DIR": stateDir,
		"SINGLESERVER_PORT":      envDefault("SINGLESERVER_PORT", "8787"),
	}
	for key, value := range defaults {
		if env[key] == "" {
			env[key] = value
		}
	}
	if env["SINGLESERVER_SETUP_TOKEN"] == "" {
		token, err := randomHex(24)
		if err != nil {
			return err
		}
		env["SINGLESERVER_SETUP_TOKEN"] = token
	}
	return writeServiceEnv(env)
}

func selectCloudflareAccount(client *CloudflareClient, accountID string, interactive bool, w io.Writer) (string, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID != "" {
		return accountID, nil
	}
	zones, err := client.allZones()
	if err != nil {
		return "", err
	}
	if interactive {
		if selected, ok, err := promptCloudflareAccount(zones, w); ok || err != nil {
			return selected, err
		}
	}
	return accountIDFromZones(zones)
}

func promptCloudflareAccount(zones []cloudflareZone, w io.Writer) (string, bool, error) {
	accounts := map[string]string{}
	for _, zone := range zones {
		id := strings.TrimSpace(zone.Account.ID)
		if id == "" {
			continue
		}
		name := strings.TrimSpace(zone.Account.Name)
		if name == "" {
			name = id
		}
		accounts[id] = name
	}
	if len(accounts) <= 1 {
		return "", false, nil
	}
	ids := sortedEnvKeys(accounts)
	fmt.Fprintln(w, "Cloudflare accounts:")
	for i, id := range ids {
		fmt.Fprintf(w, "  %d. %s (%s)\n", i+1, accounts[id], id)
	}
	p := interactivePrompter(w)
	for {
		value, err := p.askRequired("Cloudflare account")
		if err != nil {
			return "", true, err
		}
		if n, parseErr := strconv.Atoi(value); parseErr == nil && n >= 1 && n <= len(ids) {
			return ids[n-1], true, nil
		}
		for _, id := range ids {
			if strings.EqualFold(value, id) || strings.EqualFold(value, accounts[id]) {
				return id, true, nil
			}
		}
		fmt.Fprintln(w, "Enter an account id, name, or number from the list.")
	}
}

func accountIDFromZones(zones []cloudflareZone) (string, error) {
	if len(zones) == 0 {
		return "", errors.New("Cloudflare token cannot access any zones")
	}
	accounts := map[string]bool{}
	for _, zone := range zones {
		if strings.TrimSpace(zone.Account.ID) != "" {
			accounts[zone.Account.ID] = true
		}
	}
	if len(accounts) == 0 {
		return "", errors.New("Cloudflare token did not expose an account id")
	}
	if len(accounts) > 1 {
		return "", errors.New("Cloudflare token can access multiple accounts; run singleserver connect cloudflare --account <id>")
	}
	for id := range accounts {
		return id, nil
	}
	return "", errors.New("Cloudflare account selection failed")
}

func loadServiceEnv() (map[string]string, error) {
	path := serviceEnvPath()
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("%s contains invalid line %q", path, line)
		}
		values[strings.TrimSpace(key)] = unquoteEnvValue(strings.TrimSpace(value))
	}
	return values, nil
}

func writeServiceEnv(values map[string]string) error {
	var builder strings.Builder
	for _, key := range sortedEnvKeys(values) {
		builder.WriteString(key)
		builder.WriteByte('=')
		builder.WriteString(shellQuote(values[key]))
		builder.WriteByte('\n')
	}
	return writeFileAtomic(serviceEnvPath(), []byte(builder.String()))
}

func serviceEnvPath() string {
	return filepath.Join(envDefault("SINGLESERVER_STATE_DIR", "/etc/singleserver"), "singleserver.env")
}

func writeCloudflaredService(configFile string) error {
	body := fmt.Sprintf(`[Unit]
Description=Single Server Cloudflare Tunnel
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/cloudflared --config %s tunnel run
Restart=always
RestartSec=2

[Install]
WantedBy=multi-user.target
`, configFile)
	if err := os.MkdirAll("/etc/systemd/system", 0755); err != nil {
		return err
	}
	return os.WriteFile("/etc/systemd/system/cloudflared-singleserver.service", []byte(body), 0644)
}

func randomHex(bytesLen int) (string, error) {
	buf := make([]byte, bytesLen)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func cloudflareFlagTakesValue(arg string) bool {
	name := strings.TrimLeft(arg, "-")
	if before, _, ok := strings.Cut(name, "="); ok {
		name = before
	}
	return name == "account" || name == "tunnel"
}

func githubFlagTakesValue(arg string) bool {
	return false
}
