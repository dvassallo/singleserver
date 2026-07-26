package singleserver

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"
)

func TestDoctorTailscaleKeyExpiry(t *testing.T) {
	soon := time.Now().Add(60 * 24 * time.Hour).Format(time.RFC3339)
	cases := []struct {
		name string
		self *tailscaleSelf
		want string
	}{
		{"tagged node never expires", &tailscaleSelf{Tags: []string{"tag:server"}, KeyExpiry: soon}, "key expiry\tok"},
		{"expiry disabled (zero time)", &tailscaleSelf{KeyExpiry: "0001-01-01T00:00:00Z"}, "key expiry\tok"},
		{"no expiry field", &tailscaleSelf{}, "key expiry\tok"},
		{"untagged with future expiry warns", &tailscaleSelf{KeyExpiry: soon}, "key expiry\tpending"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			reportTailscaleKeyExpiry(&buf, &tailscaleStatus{Self: c.self})
			if !strings.Contains(buf.String(), c.want) {
				t.Fatalf("expected %q, got: %q", c.want, buf.String())
			}
		})
	}

	// An expiring node with a Tailscale IP gets a deep link to its admin page.
	var linkBuf bytes.Buffer
	reportTailscaleKeyExpiry(&linkBuf, &tailscaleStatus{Self: &tailscaleSelf{KeyExpiry: soon, TailscaleIPs: []string{"100.64.0.1"}}})
	if !strings.Contains(linkBuf.String(), "login.tailscale.com/admin/machines/100.64.0.1") {
		t.Fatalf("expected admin deep link, got: %q", linkBuf.String())
	}
}

func TestConnectTailscaleAppendsExtraUpArgsFromEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SINGLESERVER_STATE_DIR", dir)
	t.Setenv("SINGLESERVER_CONFIG", dir+"/apps.yml")
	t.Setenv("SINGLESERVER_REPOS_ROOT", dir+"/repos")
	t.Setenv("SINGLESERVER_STORAGE_ROOT", dir+"/storage")
	t.Setenv("SINGLESERVER_BACKUP_DIR", dir+"/backups")
	t.Setenv("SINGLESERVER_TAILSCALE_UP_EXTRA_ARGS", "--accept-dns=false --timeout=30s")

	originalOutput := commandOutputFunc
	originalRun := commandRunFunc
	originalRunToWriter := commandRunToWriterFunc
	originalFunnelReady := tailscaleFunnelReadyFunc
	t.Cleanup(func() {
		commandOutputFunc = originalOutput
		commandRunFunc = originalRun
		commandRunToWriterFunc = originalRunToWriter
		tailscaleFunnelReadyFunc = originalFunnelReady
	})

	running := false
	var upArgs []string
	commandOutputFunc = func(timeout time.Duration, name string, args ...string) (string, error) {
		if name == "tailscale" && len(args) > 0 && args[0] == "version" {
			return "1.0.0", nil
		}
		if name == "tailscale" && len(args) >= 2 && args[0] == "status" && args[1] == "--json" {
			if running {
				return `{"BackendState":"Running","Self":{"DNSName":"demo.ts.net.","HostName":"demo"}}`, nil
			}
			return `{"BackendState":"NeedsLogin"}`, nil
		}
		return "", nil
	}
	commandRunFunc = func(timeout time.Duration, name string, args ...string) error {
		if name == "tailscale" && len(args) > 0 && args[0] == "up" {
			upArgs = append([]string{}, args...)
			running = true
		}
		return nil
	}
	commandRunToWriterFunc = func(w io.Writer, timeout time.Duration, name string, args ...string) error {
		return nil
	}
	tailscaleFunnelReadyFunc = func(url string, timeout time.Duration) error {
		return nil
	}

	var out bytes.Buffer
	if err := cliTailscaleConnect([]string{"--auth-key", "tskey-auth-test", "--hostname", "demo"}, &out); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(upArgs, " ")
	for _, want := range []string{"up", "--ssh", "--auth-key=tskey-auth-test", "--hostname=demo", "--accept-dns=false", "--timeout=30s"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected tailscale up args to contain %q, got %q", want, got)
		}
	}
}
