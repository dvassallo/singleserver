package singleserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func withFakeTailscaleAPI(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	server := httptest.NewServer(handler)
	original := tailscaleAPIBaseURL
	tailscaleAPIBaseURL = server.URL
	t.Cleanup(func() {
		tailscaleAPIBaseURL = original
		server.Close()
	})
}

func TestTailscaleAPIToken(t *testing.T) {
	withFakeTailscaleAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/oauth/token" {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil || r.PostForm.Get("client_id") != "id" || r.PostForm.Get("client_secret") != "secret" {
			http.Error(w, "bad credentials", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "tok"})
	})

	token, err := tailscaleAPIToken("id", "secret")
	if err != nil || token != "tok" {
		t.Fatalf("expected token, got %q, %v", token, err)
	}
	if _, err := tailscaleAPIToken("id", "wrong"); err == nil {
		t.Fatal("expected error for bad credentials")
	}
}

func TestEnsureVIPServicePreservesAddrs(t *testing.T) {
	var putBody vipService
	withFakeTailscaleAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(vipService{Name: "svc:app", Addrs: []string{"100.99.0.1"}, Tags: []string{tailscaleServiceTag}})
		case http.MethodPut:
			if err := json.NewDecoder(r.Body).Decode(&putBody); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	})

	created, err := ensureVIPService("tok", "app", "Single Server app app")
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("adopting an existing service is not creating one")
	}
	if putBody.Name != "svc:app" || len(putBody.Addrs) != 1 || putBody.Addrs[0] != "100.99.0.1" {
		t.Fatalf("expected existing addrs preserved, got %+v", putBody)
	}
	if len(putBody.Tags) != 1 || putBody.Tags[0] != tailscaleServiceTag {
		t.Fatalf("expected service tagged %s, got %+v", tailscaleServiceTag, putBody.Tags)
	}
	if len(putBody.Ports) != 1 || putBody.Ports[0] != "tcp:443" {
		t.Fatalf("expected tcp:443 port, got %+v", putBody.Ports)
	}
}

func TestEnsureVIPServiceCreatesWhenMissing(t *testing.T) {
	withFakeTailscaleAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			http.NotFound(w, r)
		case http.MethodPut:
			w.WriteHeader(http.StatusCreated)
		default:
			http.NotFound(w, r)
		}
	})

	created, err := ensureVIPService("tok", "app", "Single Server app app")
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("a service made from a 404 was created")
	}
}

func TestEnsureVIPServiceRefusesForeignService(t *testing.T) {
	withFakeTailscaleAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			t.Fatal("a foreign service must not be overwritten")
		}
		_ = json.NewEncoder(w).Encode(vipService{Name: "svc:app", Tags: []string{"tag:other"}})
	})

	_, err := ensureVIPService("tok", "app", "Single Server app app")
	if err == nil || !strings.Contains(err.Error(), "not created by Single Server") {
		t.Fatalf("expected a foreign-service error, got: %v", err)
	}
}

func TestEnsureVIPServiceFailsOnTransientGetError(t *testing.T) {
	withFakeTailscaleAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			t.Fatal("a PUT after a failed GET could drop the service's addrs")
		}
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	})

	_, err := ensureVIPService("tok", "app", "Single Server app app")
	if err == nil || !strings.Contains(err.Error(), "HTTP 429") {
		t.Fatalf("expected the GET failure surfaced, got: %v", err)
	}
}

func TestDeleteVIPServiceTolerates404(t *testing.T) {
	withFakeTailscaleAPI(t, func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	if err := deleteVIPService("tok", "gone"); err != nil {
		t.Fatal(err)
	}
}

func TestPromptTailscaleOAuthClient(t *testing.T) {
	origInteractive := addPromptInteractiveFunc
	origInput := addPromptInput
	t.Cleanup(func() {
		addPromptInteractiveFunc = origInteractive
		addPromptInput = origInput
	})

	addPromptInteractiveFunc = func() bool { return false }
	if id, secret, err := promptTailscaleOAuthClient(&bytes.Buffer{}); err != nil || id != "" || secret != "" {
		t.Fatalf("expected silent skip when not interactive, got %q/%q, %v", id, secret, err)
	}

	withFakeTailscaleAPI(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.PostForm.Get("client_secret") != "good" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "tok"})
	})

	addPromptInteractiveFunc = func() bool { return true }
	addPromptInput = strings.NewReader("my-id\nbad\nmy-id\ngood\n")
	var out bytes.Buffer
	id, secret, err := promptTailscaleOAuthClient(&out)
	if err != nil {
		t.Fatal(err)
	}
	if id != "my-id" || secret != "good" {
		t.Fatalf("expected validated credentials after retry, got %q/%q", id, secret)
	}
	for _, want := range []string{"autoApprovers", "Trust credentials", "didn't work"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("expected output to contain %q:\n%s", want, out.String())
		}
	}

	addPromptInput = strings.NewReader("\n")
	if id, _, err := promptTailscaleOAuthClient(&bytes.Buffer{}); err != nil || id != "" {
		t.Fatalf("expected empty on skip, got %q, %v", id, err)
	}
}

func TestTailscaleServiceNameDerivation(t *testing.T) {
	cases := []struct {
		name string
		app  AppConfig
		want string
	}{
		{"first label of the domain", AppConfig{Name: "scoreboard", Hosts: []string{"scores.corp.ts.net"}}, "scores"},
		{"bare label domain", AppConfig{Name: "scoreboard", Hosts: []string{"scores"}}, "scores"},
		{"app name without a domain", AppConfig{Name: "scoreboard"}, "scoreboard"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tailscaleServiceName(tc.app); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTailscaleServiceNameForHostFollowsTheHostBeingSynced(t *testing.T) {
	if got := tailscaleServiceNameForHost("scores.corp.ts.net", "scoreboard"); got != "scores" {
		t.Fatalf("got %q, want %q", got, "scores")
	}
	if got := tailscaleServiceNameForHost("", "scoreboard"); got != "scoreboard" {
		t.Fatalf("got %q, want %q", got, "scoreboard")
	}
}

func stubTailscaleServeCommands(t *testing.T, failOn ...string) *[][]string {
	t.Helper()
	original := commandRunFunc
	t.Cleanup(func() { commandRunFunc = original })
	calls := &[][]string{}
	commandRunFunc = func(timeout time.Duration, name string, args ...string) error {
		if name != "tailscale" {
			return nil
		}
		*calls = append(*calls, args)
		joined := strings.Join(args, " ")
		for _, fail := range failOn {
			if strings.Contains(joined, fail) {
				return fmt.Errorf("tailscale %s: exit status 1", joined)
			}
		}
		return nil
	}
	return calls
}

func TestUnserveTailscaleServiceReportsFailureToStopServing(t *testing.T) {
	calls := stubTailscaleServeCommands(t, "off")

	var out bytes.Buffer
	err := unserveTailscaleService("scoreboard", "scores", &out)
	if err == nil {
		t.Fatal("expected an error when `tailscale serve ... off` fails")
	}
	for _, want := range []string{"svc:scores", "may still serve it"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected error to mention %q, got: %v", want, err)
		}
	}
	if len(*calls) != 2 {
		t.Fatalf("expected drain then off, got %+v", *calls)
	}
}

func TestUnserveTailscaleServiceToleratesDrainFailure(t *testing.T) {
	calls := stubTailscaleServeCommands(t, "drain")

	var out bytes.Buffer
	if err := unserveTailscaleService("scoreboard", "scores", &out); err != nil {
		t.Fatalf("expected a failed drain to be tolerated, got: %v", err)
	}
	if len(*calls) != 2 {
		t.Fatalf("expected off to run after a failed drain, got %+v", *calls)
	}
	if joined := strings.Join((*calls)[1], " "); !strings.Contains(joined, "off") {
		t.Fatalf("expected the second call to turn the service off, got %q", joined)
	}
}

func TestSyncTailscaleAppDomainRemovalFailsWhenServiceKeepsServing(t *testing.T) {
	t.Setenv("SINGLESERVER_STATE_DIR", t.TempDir())
	t.Setenv("TAILSCALE_OAUTH_CLIENT_ID", "")
	t.Setenv("TAILSCALE_OAUTH_CLIENT_SECRET", "")
	stubTailscaleServeCommands(t, "off")

	app := AppConfig{Name: "scoreboard", Hosts: []string{"scores.corp.ts.net"}, Tunnel: "private"}
	var out bytes.Buffer
	err := syncTailscaleAppDomain(app, "scores.corp.ts.net", false, &out)
	if err == nil {
		t.Fatalf("expected removal to fail when the service keeps serving, output:\n%s", out.String())
	}
	if !strings.Contains(err.Error(), "svc:scores") {
		t.Fatalf("expected the error to name the service, got: %v", err)
	}
}
