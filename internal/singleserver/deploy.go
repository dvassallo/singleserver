package singleserver

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type DeployManager struct {
	logger *log.Logger
	github *GitHubClient

	mu     sync.Mutex
	queues map[string]chan DeployRequest
}

type DeployRequest struct {
	App            AppConfig
	Repo           string
	Branch         string
	SHA            string
	InstallationID int64
	RunID          string
}

func NewDeployManager(logger *log.Logger, github *GitHubClient) *DeployManager {
	return &DeployManager{
		logger: logger,
		github: github,
		queues: map[string]chan DeployRequest{},
	}
}

func (m *DeployManager) Enqueue(req DeployRequest) string {
	if req.RunID == "" {
		req.RunID = fmt.Sprintf("%s-%d", req.App.Name, time.Now().UnixMilli())
	}

	m.mu.Lock()
	queue := m.queues[req.App.Name]
	if queue == nil {
		queue = make(chan DeployRequest, 32)
		m.queues[req.App.Name] = queue
		go m.worker(req.App.Name, queue)
	}
	m.mu.Unlock()

	queue <- req
	return req.RunID
}

func (m *DeployManager) worker(appName string, queue <-chan DeployRequest) {
	for req := range queue {
		_ = m.run(req)
	}
}

func (m *DeployManager) run(req DeployRequest) error {
	start := time.Now()
	m.logger.Printf("[deploy:%s] start %s@%s (%s) -> %s", req.RunID, req.Repo, req.SHA, req.Branch, req.App.Name)

	token, err := m.github.DeployToken(req.InstallationID)
	if err != nil {
		m.logger.Printf("[deploy:%s] failed to get GitHub token: %v", req.RunID, err)
		return err
	}

	_ = m.github.CreateCommitStatus(req.Repo, req.SHA, token, "pending", "Single Server deploying "+req.App.Name)

	timing, err := m.runKamal(req, token)
	if err == nil {
		err = m.runHealthcheck(req.App, req.RunID)
	}
	if err != nil {
		_ = m.github.CreateCommitStatus(req.Repo, req.SHA, token, "failure", "Single Server deploy failed: "+err.Error())
		m.logger.Printf("[deploy:%s] failed after %dms: %v", req.RunID, time.Since(start).Milliseconds(), err)
		return err
	}

	_ = m.github.CreateCommitStatus(req.Repo, req.SHA, token, "success", fmt.Sprintf("Single Server deployed in %dms", timing.TotalMS))
	m.logger.Printf("[deploy:%s] success total_ms=%d", req.RunID, timing.TotalMS)
	return nil
}

type DeployTiming struct {
	TotalMS int64
	Line    string
}

func (m *DeployManager) runKamal(req DeployRequest, token string) (DeployTiming, error) {
	secretKeys, err := appSecretKeys(req.App.Name)
	if err != nil {
		return DeployTiming{}, err
	}
	req.App.SecretEnvKeys = secretKeys
	generatedDeployYAML, err := GeneratedDeployYAML(req.App)
	if err != nil {
		return DeployTiming{}, err
	}

	script := `
set -euo pipefail

now_ms() {
  echo "$(($(date +%s%N) / 1000000))"
}

start_ms=$(now_ms)
repo_dir="$SINGLESERVER_REPO_DIR"
repo="$SINGLESERVER_REPO"
sha="$SINGLESERVER_SHA"
app_name="$SINGLESERVER_APP_NAME"
env_file="$SINGLESERVER_ENV_FILE"
remote_url="https://x-access-token:${SINGLESERVER_GITHUB_TOKEN}@github.com/${repo}.git"
generated_deploy_file=""

cleanup() {
  if [ -n "$generated_deploy_file" ]; then
    rm -f "$generated_deploy_file"
    rmdir config 2>/dev/null || true
  fi
}
trap cleanup EXIT

mkdir -p "$(dirname "$repo_dir")"
if [ ! -d "$repo_dir/.git" ]; then
  rm -rf "$repo_dir"
  git clone --depth=1 "$remote_url" "$repo_dir"
fi

cd "$repo_dir"
git remote set-url origin "$remote_url"
git fetch --depth=1 origin "$sha"
git reset --hard "$sha"
git remote set-url origin "https://github.com/${repo}.git"

git_done_ms=$(now_ms)

if git ls-files --error-unmatch config/deploy.yml >/dev/null 2>&1; then
  deploy_config_source=repo
else
  if ! grep -qxF "/config/deploy.yml" .git/info/exclude; then
    printf '\n/config/deploy.yml\n' >> .git/info/exclude
  fi
  rm -f config/deploy.yml
  mkdir -p config
  generated_deploy_file=config/deploy.yml
  printf '%s' "$SINGLESERVER_GENERATED_DEPLOY_YML" > "$generated_deploy_file"
  deploy_config_source=generated
fi
echo "deploy_config=${deploy_config_source}"

mkdir -p .kamal
if ! grep -qxF "/.kamal/secrets" .git/info/exclude; then
  printf '\n/.kamal/secrets\n' >> .git/info/exclude
fi
if [ -f "$env_file" ]; then
  install -m 600 "$env_file" .kamal/secrets
else
  rm -f .kamal/secrets
fi

if docker ps -a --format '{{.Names}}' | grep -Eq "^${app_name}-"; then
  kamal_command=redeploy
else
  kamal_command=setup
fi

GITHUB_SHA="$sha" kamal "$kamal_command" -q
end_ms=$(now_ms)
echo "timing command=${kamal_command} config=${deploy_config_source} git_ms=$((git_done_ms - start_ms)) kamal_ms=$((end_ms - git_done_ms)) total_ms=$((end_ms - start_ms))"
`

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	command := exec.CommandContext(ctx, "bash", "-lc", script)
	command.Env = append(os.Environ(),
		"SINGLESERVER_APP_NAME="+req.App.Name,
		"SINGLESERVER_REPO_DIR="+req.App.RepoDir,
		"SINGLESERVER_REPO="+req.Repo,
		"SINGLESERVER_SHA="+req.SHA,
		"SINGLESERVER_GITHUB_TOKEN="+token,
		"SINGLESERVER_GENERATED_DEPLOY_YML="+string(generatedDeployYAML),
		"SINGLESERVER_ENV_FILE="+appEnvPath(req.App.Name),
	)

	var combined lockedBuffer
	command.Stdout = &lineLogger{prefix: "[deploy:" + req.RunID + "] out: ", logger: m.logger, sink: &combined}
	command.Stderr = &lineLogger{prefix: "[deploy:" + req.RunID + "] err: ", logger: m.logger, sink: &combined}

	start := time.Now()
	err = command.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return DeployTiming{}, fmt.Errorf("deploy timed out")
	}
	if err != nil {
		return DeployTiming{}, err
	}

	output := combined.String()
	timingLine := ""
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "timing ") {
			timingLine = line
			break
		}
	}
	totalMS := time.Since(start).Milliseconds()
	if match := regexp.MustCompile(`total_ms=(\d+)`).FindStringSubmatch(timingLine); len(match) == 2 {
		if parsed, parseErr := strconv.ParseInt(match[1], 10, 64); parseErr == nil {
			totalMS = parsed
		}
	}
	return DeployTiming{TotalMS: totalMS, Line: timingLine}, nil
}

func (m *DeployManager) runHealthcheck(app AppConfig, runID string) error {
	if app.Healthcheck == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, app.Healthcheck, nil)
	if err != nil {
		return err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 400 {
		return fmt.Errorf("healthcheck %s returned %d", app.Healthcheck, res.StatusCode)
	}
	m.logger.Printf("[deploy:%s] healthcheck ok %s", runID, app.Healthcheck)
	return nil
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type lineLogger struct {
	prefix string
	logger *log.Logger
	sink   *lockedBuffer
	buf    bytes.Buffer
}

func (l *lineLogger) Write(p []byte) (int, error) {
	_, _ = l.sink.Write(p)
	for _, b := range p {
		if b == '\n' {
			l.flush()
			continue
		}
		_ = l.buf.WriteByte(b)
	}
	return len(p), nil
}

func (l *lineLogger) flush() {
	line := redact(l.buf.String())
	l.buf.Reset()
	if line != "" {
		l.logger.Print(l.prefix + line)
	}
}

func redact(line string) string {
	return regexp.MustCompile(`x-access-token:[^@]+@github\.com`).ReplaceAllString(line, "x-access-token:REDACTED@github.com")
}
