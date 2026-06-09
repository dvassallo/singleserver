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
			writeCheck(w, "tailscale", "login", "pending", "run `tailscale up --ssh` on this server, then run `singleserver tailscale connect`")
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
		writeCheck(w, "tailscale", "login", "pending", "run `tailscale up --ssh` on this server, then run `singleserver tailscale connect`")
		return nil
	}
	writeCheck(w, "tailscale", "status", "ok", tailscaleStatusName(status))

	if err := commandRunFunc(15*time.Second, "tailscale", "set", "--ssh"); err != nil {
		writeCheck(w, "tailscale", "ssh", "pending", err.Error())
	} else {
		writeCheck(w, "tailscale", "ssh", "ok", "-")
	}

	port := envDefault("SINGLESERVER_PORT", "8787")
	writeCheck(w, "tailscale", "funnel", "starting", "127.0.0.1:"+port)
	if err := commandRunToWriterFunc(w, 45*time.Second, "tailscale", "funnel", "--bg", "--yes", port); err != nil {
		writeCheck(w, "tailscale", "funnel", "pending", err.Error())
		return writeTailscaleStateFromStatus(status, "")
	}
	status, err = currentTailscaleStatus()
	if err != nil {
		return err
	}
	funnelURL := tailscaleFunnelURL(status)
	if funnelURL == "" {
		writeCheck(w, "tailscale", "funnel", "pending", "-", "could not determine Funnel URL from tailscale status")
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
	writeCheck(w, "tailscale", "funnel", "ok", funnelURL, "target=127.0.0.1:"+port)
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
		writeCheck(w, "tailscale", "setup", status, "install Tailscale", err.Error())
		return appCount == 0
	}
	status, err := currentTailscaleStatus()
	if err != nil || !tailscaleRunning(status) {
		state := "pending"
		if appCount > 0 {
			state = "failed"
		}
		if err != nil {
			writeCheck(w, "tailscale", "setup", state, "run `tailscale up --ssh`", err.Error())
		} else {
			writeCheck(w, "tailscale", "setup", state, "run `tailscale up --ssh`")
		}
		return appCount == 0
	}
	writeCheck(w, "tailscale", "status", "ok", tailscaleStatusName(status))

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
		writeCheck(w, "tailscale", "funnel", status, "run `singleserver tailscale connect`")
		return appCount == 0
	}
	parsed, err := url.Parse(publicURL)
	if err != nil || parsed.Scheme != "https" || !strings.HasSuffix(parsed.Hostname(), ".ts.net") {
		writeCheck(w, "tailscale", "funnel", "failed", publicURL, "expected Tailscale Funnel URL")
		return false
	}
	if err := commandRunFunc(5*time.Second, "tailscale", "funnel", "status", "--json"); err != nil {
		status := "pending"
		if appCount > 0 {
			status = "failed"
		}
		writeCheck(w, "tailscale", "funnel", status, err.Error())
		return appCount == 0
	}
	writeCheck(w, "tailscale", "funnel", "ok", publicURL)
	return true
}
