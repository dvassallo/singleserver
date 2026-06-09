package singleserver

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func cliGitHubConnect(args []string, w io.Writer) error {
	fs := flag.NewFlagSet("github connect", flag.ContinueOnError)
	fs.SetOutput(w)
	if err := fs.Parse(normalizeFlagArgs(args, githubFlagTakesValue)); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: singleserver github connect")
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
	fmt.Fprintf(w, "github\tconnect\t%s/setup/github-app?token=%s\n", publicURL, token)
	return nil
}

func cliCloudflareConnect(args []string, w io.Writer) error {
	fs := flag.NewFlagSet("cloudflare connect", flag.ContinueOnError)
	fs.SetOutput(w)
	zoneName := fs.String("zone", defaultCloudflareZone(), "Cloudflare zone name")
	tunnelName := fs.String("tunnel", "", "Cloudflare tunnel name")
	if err := fs.Parse(normalizeFlagArgs(args, cloudflareFlagTakesValue)); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: singleserver cloudflare connect [--zone example.com] [--tunnel <name>]")
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
	client, err := newCloudflareClient(token)
	if err != nil {
		return err
	}

	zone, err := selectCloudflareZone(client, *zoneName)
	if err != nil {
		return err
	}
	state.APIToken = token
	state.AccountID = zone.Account.ID
	state.ZoneID = zone.ID
	state.ZoneName = zone.Name
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
			fmt.Fprintf(w, "cloudflare\ttunnel\tok\t%s\treused %s\n", state.TunnelID, state.TunnelName)
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
			fmt.Fprintf(w, "cloudflare\ttunnel\tok\t%s\tcreated %s\n", state.TunnelID, state.TunnelName)
		}
	} else {
		fmt.Fprintf(w, "cloudflare\ttunnel\tok\t%s\n", state.TunnelID)
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
	fmt.Fprintf(w, "cloudflare\tingress\tok\tapps -> %s.cfargotunnel.com\n", state.TunnelID)
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
	if err := os.MkdirAll("/srv/repos", 0755); err != nil {
		return err
	}
	if err := os.MkdirAll("/srv/storage", 0755); err != nil {
		return err
	}
	if err := os.MkdirAll("/srv/backups", 0755); err != nil {
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

func selectCloudflareZone(client *CloudflareClient, name string) (*cloudflareZone, error) {
	zones, err := client.zones(name)
	if err != nil {
		return nil, err
	}
	if len(zones) == 0 {
		if strings.TrimSpace(name) == "" {
			return nil, errors.New("Cloudflare token cannot access any zones")
		}
		return nil, fmt.Errorf("Cloudflare token cannot access zone %s", name)
	}
	if len(zones) > 1 && strings.TrimSpace(name) == "" {
		return nil, errors.New("Cloudflare token can access multiple zones; run singleserver cloudflare connect --zone <domain>")
	}
	return &zones[0], nil
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
	return name == "zone" || name == "tunnel"
}

func githubFlagTakesValue(arg string) bool {
	return false
}

func defaultCloudflareZone() string {
	if value := strings.TrimSpace(os.Getenv("SINGLESERVER_CLOUDFLARE_ZONE")); value != "" {
		return value
	}
	return strings.TrimSpace(os.Getenv("CLOUDFLARE_ZONE"))
}
