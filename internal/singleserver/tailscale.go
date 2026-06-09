package singleserver

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type TailscaleState struct {
	Hostname  string `json:"hostname"`
	FunnelURL string `json:"funnel_url"`
}

type tailscaleStatus struct {
	BackendState string `json:"BackendState"`
	Self         *struct {
		DNSName      string   `json:"DNSName"`
		HostName     string   `json:"HostName"`
		TailscaleIPs []string `json:"TailscaleIPs"`
	} `json:"Self"`
}

func cliTailscaleConnect(args []string, w io.Writer) error {
	fs := flag.NewFlagSet("tailscale connect", flag.ContinueOnError)
	fs.SetOutput(w)
	authKey := fs.String("auth-key", defaultTailscaleAuthKey(), "Tailscale auth key")
	hostname := fs.String("hostname", strings.TrimSpace(os.Getenv("SINGLESERVER_TAILSCALE_HOSTNAME")), "Tailscale hostname")
	if err := fs.Parse(normalizeFlagArgs(args, tailscaleFlagTakesValue)); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: singleserver tailscale connect [--auth-key <key>] [--hostname <name>]")
	}
	if err := ensureBaseFiles(); err != nil {
		return err
	}
	if _, err := commandOutputFunc(5*time.Second, "tailscale", "version"); err != nil {
		return fmt.Errorf("tailscale is not installed; rerun the Single Server installer: %w", err)
	}
	if err := commandRunFunc(20*time.Second, "systemctl", "enable", "--now", "tailscaled"); err != nil {
		return err
	}

	status, err := currentTailscaleStatus()
	if err != nil || !tailscaleRunning(status) {
		if strings.TrimSpace(*authKey) == "" {
			fmt.Fprintln(w, "tailscale\tlogin\tpending\trun `tailscale up --ssh` on this server, then run `singleserver tailscale connect`")
			return nil
		}
		upArgs := []string{"up", "--ssh", "--auth-key=" + strings.TrimSpace(*authKey)}
		if strings.TrimSpace(*hostname) != "" {
			upArgs = append(upArgs, "--hostname="+strings.TrimSpace(*hostname))
		}
		if err := commandRunFunc(2*time.Minute, "tailscale", upArgs...); err != nil {
			return err
		}
		status, err = currentTailscaleStatus()
		if err != nil {
			return err
		}
	}
	if !tailscaleRunning(status) {
		fmt.Fprintln(w, "tailscale\tlogin\tpending\trun `tailscale up --ssh` on this server, then run `singleserver tailscale connect`")
		return nil
	}
	fmt.Fprintf(w, "tailscale\tstatus\tok\t%s\n", tailscaleStatusName(status))

	if err := commandRunFunc(15*time.Second, "tailscale", "set", "--ssh"); err != nil {
		fmt.Fprintf(w, "tailscale\tssh\tpending\t%s\n", err)
	} else {
		fmt.Fprintln(w, "tailscale\tssh\tok")
	}

	port := envDefault("SINGLESERVER_PORT", "8787")
	if err := commandRunFunc(30*time.Second, "tailscale", "funnel", "--bg", "--yes", port); err != nil {
		fmt.Fprintf(w, "tailscale\tfunnel\tpending\t%s\n", err)
		return writeTailscaleStateFromStatus(status, "")
	}
	status, err = currentTailscaleStatus()
	if err != nil {
		return err
	}
	funnelURL := tailscaleFunnelURL(status)
	if funnelURL == "" {
		fmt.Fprintln(w, "tailscale\tfunnel\tpending\tcould not determine Funnel URL from tailscale status")
		return writeTailscaleStateFromStatus(status, "")
	}
	if err := writeTailscaleStateFromStatus(status, funnelURL); err != nil {
		return err
	}
	env, err := loadServiceEnv()
	if err != nil {
		return err
	}
	env["SINGLESERVER_PUBLIC_URL"] = funnelURL
	if err := writeServiceEnv(env); err != nil {
		return err
	}
	if err := commandRunFunc(10*time.Second, "systemctl", "restart", "singleserver.service"); err != nil {
		return err
	}
	fmt.Fprintf(w, "tailscale\tfunnel\tok\t%s -> 127.0.0.1:%s\n", funnelURL, port)
	return nil
}

func currentTailscaleStatus() (*tailscaleStatus, error) {
	body, err := commandOutputFunc(5*time.Second, "tailscale", "status", "--json")
	if err != nil {
		return nil, err
	}
	var status tailscaleStatus
	if err := json.Unmarshal([]byte(body), &status); err != nil {
		return nil, err
	}
	return &status, nil
}

func tailscaleRunning(status *tailscaleStatus) bool {
	return status != nil && strings.EqualFold(status.BackendState, "Running") && status.Self != nil
}

func tailscaleStatusName(status *tailscaleStatus) string {
	if status == nil || status.Self == nil {
		return "-"
	}
	if name := strings.TrimSuffix(strings.TrimSpace(status.Self.DNSName), "."); name != "" {
		return name
	}
	if name := strings.TrimSpace(status.Self.HostName); name != "" {
		return name
	}
	return "-"
}

func tailscaleFunnelURL(status *tailscaleStatus) string {
	host := tailscaleStatusName(status)
	if host == "-" || !strings.Contains(host, ".ts.net") {
		return ""
	}
	return "https://" + host
}

func writeTailscaleStateFromStatus(status *tailscaleStatus, funnelURL string) error {
	state := &TailscaleState{
		Hostname:  tailscaleStatusName(status),
		FunnelURL: strings.TrimRight(funnelURL, "/"),
	}
	return writeTailscaleState(state)
}

func loadTailscaleState() (*TailscaleState, error) {
	body, err := os.ReadFile(tailscaleStatePath())
	if err != nil {
		if os.IsNotExist(err) {
			return &TailscaleState{}, nil
		}
		return nil, err
	}
	var state TailscaleState
	if err := json.Unmarshal(body, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func writeTailscaleState(state *TailscaleState) error {
	body, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(tailscaleStatePath(), append(body, '\n'))
}

func tailscaleStatePath() string {
	return filepath.Join(envDefault("SINGLESERVER_STATE_DIR", "/etc/singleserver"), "tailscale.json")
}

func tailscaleFlagTakesValue(arg string) bool {
	name := strings.TrimLeft(arg, "-")
	if before, _, ok := strings.Cut(name, "="); ok {
		name = before
	}
	return name == "auth-key" || name == "hostname"
}

func defaultTailscaleAuthKey() string {
	if value := strings.TrimSpace(os.Getenv("TAILSCALE_AUTHKEY")); value != "" {
		return value
	}
	return strings.TrimSpace(os.Getenv("TS_AUTHKEY"))
}

func doctorTailscale(w io.Writer, appCount int) bool {
	if _, err := commandOutputFunc(5*time.Second, "tailscale", "version"); err != nil {
		status := "pending"
		if appCount > 0 {
			status = "failed"
		}
		fmt.Fprintf(w, "tailscale\tsetup\t%s\tinstall Tailscale\t%s\n", status, err)
		return appCount == 0
	}
	status, err := currentTailscaleStatus()
	if err != nil || !tailscaleRunning(status) {
		state := "pending"
		if appCount > 0 {
			state = "failed"
		}
		if err != nil {
			fmt.Fprintf(w, "tailscale\tsetup\t%s\trun `tailscale up --ssh`\t%s\n", state, err)
		} else {
			fmt.Fprintf(w, "tailscale\tsetup\t%s\trun `tailscale up --ssh`\n", state)
		}
		return appCount == 0
	}
	fmt.Fprintf(w, "tailscale\tstatus\tok\t%s\n", tailscaleStatusName(status))

	env, _ := loadServiceEnv()
	publicURL := strings.TrimRight(env["SINGLESERVER_PUBLIC_URL"], "/")
	if publicURL == "" {
		state, _ := loadTailscaleState()
		publicURL = strings.TrimRight(state.FunnelURL, "/")
	}
	if publicURL == "" {
		status := "pending"
		if appCount > 0 {
			status = "failed"
		}
		fmt.Fprintf(w, "tailscale\tfunnel\t%s\trun `singleserver tailscale connect`\n", status)
		return appCount == 0
	}
	parsed, err := url.Parse(publicURL)
	if err != nil || parsed.Scheme != "https" || !strings.HasSuffix(parsed.Hostname(), ".ts.net") {
		fmt.Fprintf(w, "tailscale\tfunnel\tfailed\texpected Tailscale Funnel URL, got %s\n", publicURL)
		return false
	}
	if err := commandRunFunc(5*time.Second, "tailscale", "funnel", "status", "--json"); err != nil {
		status := "pending"
		if appCount > 0 {
			status = "failed"
		}
		fmt.Fprintf(w, "tailscale\tfunnel\t%s\t%s\n", status, err)
		return appCount == 0
	}
	fmt.Fprintf(w, "tailscale\tfunnel\tok\t%s\n", publicURL)
	return true
}
