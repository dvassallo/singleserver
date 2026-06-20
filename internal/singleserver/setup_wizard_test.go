package singleserver

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stubTailscaleNotRunning makes status/command lookups report a box that has
// joined nothing yet, so the wizard takes its "needs connecting" branches.
func stubTailscaleNotRunning(t *testing.T) {
	t.Helper()
	origOut := commandOutputFunc
	origRun := commandRunFunc
	t.Cleanup(func() {
		commandOutputFunc = origOut
		commandRunFunc = origRun
	})
	commandOutputFunc = func(timeout time.Duration, name string, args ...string) (string, error) {
		return `{"BackendState":"Stopped"}`, nil
	}
	commandRunFunc = func(timeout time.Duration, name string, args ...string) error { return nil }
}

func setupWizardEnv(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("SINGLESERVER_STATE_DIR", dir)
	t.Setenv("SINGLESERVER_CONFIG", filepath.Join(dir, "apps.yml"))
	t.Setenv("CLOUDFLARE_API_TOKEN", "")
	t.Setenv("TAILSCALE_AUTHKEY", "")
	setBaseDirsForTest(t, dir)
	stubTailscaleNotRunning(t)
	return dir
}

func TestSetupNonInteractiveReportsPending(t *testing.T) {
	setupWizardEnv(t)
	origInteractive := addPromptInteractiveFunc
	t.Cleanup(func() { addPromptInteractiveFunc = origInteractive })
	addPromptInteractiveFunc = func() bool { return false }

	var buf bytes.Buffer
	out := newTextOutput(&buf)
	if err := cliSetup(nil, out); err != nil {
		t.Fatal(err)
	}
	out.Flush()

	got := buf.String()
	for _, want := range []string{
		"1/3", "Tailscale", "2/3", "Cloudflare", "3/3", "GitHub",
		"login", "pending", "token", "app",
		"Setup is not finished yet",
		"singleserver connect tailscale",
		"singleserver connect cloudflare",
		"singleserver connect github",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in output:\n%s", want, got)
		}
	}
}

func TestSetupInteractiveDeclineSkips(t *testing.T) {
	setupWizardEnv(t)

	origInteractive := addPromptInteractiveFunc
	origInput := addPromptInput
	origUp := tailscaleUpInteractiveFunc
	t.Cleanup(func() {
		addPromptInteractiveFunc = origInteractive
		addPromptInput = origInput
		tailscaleUpInteractiveFunc = origUp
	})
	addPromptInteractiveFunc = func() bool { return true }
	addPromptInput = strings.NewReader("n\nn\n")
	upCalls := 0
	tailscaleUpInteractiveFunc = func() error { upCalls++; return nil }

	var buf bytes.Buffer
	out := newTextOutput(&buf)
	if err := cliSetup(nil, out); err != nil {
		t.Fatal(err)
	}
	out.Flush()

	if upCalls != 0 {
		t.Fatalf("tailscale up should not run when declined, called %d times", upCalls)
	}
	got := buf.String()
	if !strings.Contains(got, "Connect Tailscale now?") || !strings.Contains(got, "Connect Cloudflare now?") {
		t.Fatalf("expected both prompts:\n%s", got)
	}
	if strings.Count(got, "skipped") < 2 {
		t.Fatalf("expected tailscale and cloudflare skipped:\n%s", got)
	}
}

func TestSetupInteractiveAcceptRunsTailscaleUp(t *testing.T) {
	setupWizardEnv(t)

	origInteractive := addPromptInteractiveFunc
	origInput := addPromptInput
	origUp := tailscaleUpInteractiveFunc
	t.Cleanup(func() {
		addPromptInteractiveFunc = origInteractive
		addPromptInput = origInput
		tailscaleUpInteractiveFunc = origUp
	})
	addPromptInteractiveFunc = func() bool { return true }
	// Tailscale yes, Cloudflare no. Stub the browser login to fail so the wizard
	// reports it pending without driving a full (network) connect.
	addPromptInput = strings.NewReader("y\nn\n")
	upCalls := 0
	tailscaleUpInteractiveFunc = func() error { upCalls++; return fmt.Errorf("login aborted") }

	var buf bytes.Buffer
	out := newTextOutput(&buf)
	if err := cliSetup(nil, out); err != nil {
		t.Fatal(err)
	}
	out.Flush()

	if upCalls != 1 {
		t.Fatalf("expected tailscale up to run once, got %d", upCalls)
	}
	if !strings.Contains(buf.String(), "pending") {
		t.Fatalf("expected a pending tailscale login after failed browser login:\n%s", buf.String())
	}
}

func TestSetupCommandRegistered(t *testing.T) {
	cmd := lookupCommand("setup")
	if cmd == nil {
		t.Fatal("setup command not registered")
	}
	if cmd.Group != "Setup" {
		t.Fatalf("setup group = %q, want Setup", cmd.Group)
	}
	if cmd.Run == nil {
		t.Fatal("setup command has no Run")
	}
}
