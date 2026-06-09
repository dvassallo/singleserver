package singleserver

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const maxWebhookBodyBytes = 2 * 1024 * 1024

type Server struct {
	logger        *log.Logger
	configPath    string
	publicURL     string
	setupToken    string
	github        *GitHubClient
	deployManager *DeployManager
}

type PushPayload struct {
	Ref          string `json:"ref"`
	After        string `json:"after"`
	Repository   Repo   `json:"repository"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
}

type Repo struct {
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
}

func Run(logger *log.Logger) error {
	stateDir := envDefault("SINGLESERVER_STATE_DIR", "/etc/singleserver")
	github := NewGitHubClient(stateDir)
	server := &Server{
		logger:        logger,
		configPath:    envDefault("SINGLESERVER_CONFIG", "/etc/singleserver/apps.yml"),
		publicURL:     strings.TrimRight(envDefault("SINGLESERVER_PUBLIC_URL", "http://127.0.0.1:"+envDefault("SINGLESERVER_PORT", "8787")), "/"),
		setupToken:    os.Getenv("SINGLESERVER_SETUP_TOKEN"),
		github:        github,
		deployManager: NewDeployManager(logger, github),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", server.handleHealth)
	mux.HandleFunc("GET /setup/github-app", server.handleSetupGitHubApp)
	mux.HandleFunc("GET /setup/callback", server.handleSetupCallback)
	mux.HandleFunc("POST /github/webhook", server.handleGitHubWebhook)

	httpServer := &http.Server{
		Addr:              "127.0.0.1:" + envDefault("SINGLESERVER_PORT", "8787"),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Printf("[server] Single Server listening on http://%s", httpServer.Addr)
		errCh <- httpServer.ListenAndServe()
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	case sig := <-sigCh:
		logger.Printf("[server] received %s, shutting down", sig)
		return httpServer.Close()
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write([]byte("OK\n"))
}

func (s *Server) handleGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBodyBytes))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_body"})
		return
	}

	webhookSecret, err := s.github.WebhookSecret()
	if err != nil {
		s.logger.Printf("[webhook] webhook secret is not configured: %v", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "webhook_secret_not_configured"})
		return
	}
	if !VerifyWebhookSignature(webhookSecret, body, r.Header.Get("X-Hub-Signature-256")) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "bad_signature"})
		return
	}

	event := r.Header.Get("X-GitHub-Event")
	delivery := r.Header.Get("X-GitHub-Delivery")
	if event == "ping" {
		s.logger.Printf("[webhook:%s] ping", delivery)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "event": "ping"})
		return
	}
	if event != "push" {
		s.logger.Printf("[webhook:%s] ignored event=%s", delivery, event)
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "ignored": true, "reason": "event " + event})
		return
	}

	var payload PushPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_json"})
		return
	}
	if payload.After == "" || strings.Trim(payload.After, "0") == "" {
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "ignored": true, "reason": "empty push"})
		return
	}
	config, err := LoadConfig(s.configPath)
	if err != nil {
		s.logger.Printf("[webhook:%s] config load failed: %v", delivery, err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "bad_config"})
		return
	}
	app, branch, reason := config.AppForPush(&payload)
	if app == nil {
		s.logger.Printf("[webhook:%s] ignored %s@%s: %s", delivery, payload.Repository.FullName, payload.After, reason)
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "ignored": true, "reason": reason})
		return
	}

	runID := s.deployManager.Enqueue(DeployRequest{
		App:            *app,
		Repo:           payload.Repository.FullName,
		Branch:         branch,
		SHA:            payload.After,
		InstallationID: payload.Installation.ID,
	})
	s.logger.Printf("[webhook:%s] accepted %s@%s as %s", delivery, payload.Repository.FullName, payload.After, runID)
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "accepted": true, "run_id": runID})
}

func (s *Server) handleSetupGitHubApp(w http.ResponseWriter, r *http.Request) {
	if !s.setupAllowed(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "bad_setup_token"})
		return
	}
	appName := "Single Server"
	manifest := map[string]any{
		"name":        appName,
		"url":         "https://singleserver.com",
		"description": "Deploy many small apps from GitHub to one server.",
		"public":      true,
		"hook_attributes": map[string]any{
			"url":    s.publicURL + "/github/webhook",
			"active": true,
		},
		"redirect_url":  s.publicURL + "/setup/callback",
		"callback_urls": []string{s.publicURL + "/setup/callback"},
		"default_permissions": map[string]string{
			"contents": "read",
			"statuses": "write",
		},
		"default_events": []string{"push"},
	}
	manifestJSON, _ := json.Marshal(manifest)
	state := s.setupToken
	fmt.Fprintf(w, `<!doctype html>
<meta charset="utf-8">
<title>Single Server GitHub App Setup</title>
<h1>Single Server GitHub App Setup</h1>
<p>This registers a public GitHub App named <strong>%s</strong>.</p>
<form action="https://github.com/settings/apps/new?state=%s" method="post">
  <input type="hidden" name="manifest" value="%s">
  <button type="submit">Create GitHub App</button>
</form>
`, html.EscapeString(appName), html.EscapeString(state), html.EscapeString(string(manifestJSON)))
}

func (s *Server) handleSetupCallback(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("state") != s.setupToken || s.setupToken == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "bad_setup_state"})
		return
	}
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if code == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing_code"})
		return
	}
	secrets, installURL, err := s.github.ConvertManifestCode(code)
	if err != nil {
		s.logger.Printf("[setup] manifest conversion failed: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "manifest_conversion_failed"})
		return
	}
	s.logger.Printf("[setup] configured GitHub App id=%d slug=%s", secrets.AppID, secrets.Slug)
	fmt.Fprintf(w, `<!doctype html>
<meta charset="utf-8">
<title>Single Server GitHub App Created</title>
<h1>Single Server GitHub App Created</h1>
<p>App ID: <code>%d</code></p>
<p>Install it on <strong>all repositories</strong>, then Single Server will use <code>apps.yml</code> as the deploy allowlist.</p>
<p><a href="%s">Install Single Server</a></p>
`, secrets.AppID, html.EscapeString(installURL))
}

func (s *Server) setupAllowed(r *http.Request) bool {
	return s.setupToken != "" && r.URL.Query().Get("token") == s.setupToken
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func envDefault(name, fallback string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	return value
}
