package singleserver

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	_ "embed"
)

//go:embed cleanup_script.sh
var postDeployCleanupScript string

const postDeployCleanupTimeout = 10 * time.Minute

// postDeployCleanupShell runs the cleanup script. Tests replace it to run the
// script against fake kamal/docker binaries.
var postDeployCleanupShell = []string{"bash", "-lc"}

// cleanupAfterDeploy prunes old containers, images and build cache once a
// deploy has succeeded. It only logs failures: a deploy that passed its
// healthcheck stays successful even when cleanup fails.
func (m *DeployManager) cleanupAfterDeploy(app AppConfig, runID string) {
	start := time.Now()
	summary, err := runPostDeployCleanup(app, runID, m.logger)
	if err != nil {
		m.logger.Printf("[deploy:%s] cleanup failed after %dms: %v", runID, time.Since(start).Milliseconds(), err)
		return
	}
	m.logger.Printf("[deploy:%s] cleanup: %s duration_ms=%d", runID, summary, time.Since(start).Milliseconds())
}

func runPostDeployCleanup(app AppConfig, runID string, logger *log.Logger) (string, error) {
	generatedDeployYAML, err := GeneratedDeployYAML(app)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), postDeployCleanupTimeout)
	defer cancel()

	args := append(append([]string{}, postDeployCleanupShell[1:]...), postDeployCleanupScript)
	command := exec.CommandContext(ctx, postDeployCleanupShell[0], args...)
	command.Env = append(os.Environ(),
		"HOME=/root",
		"SINGLESERVER_APP_NAME="+app.Name,
		"SINGLESERVER_REPO_DIR="+app.RepoDir,
		"SINGLESERVER_GENERATED_DEPLOY_YML="+string(generatedDeployYAML),
		"SINGLESERVER_ENV_FILE="+appEnvPath(app.Name),
	)

	var combined lockedBuffer
	command.Stdout = &lineLogger{prefix: "[deploy:" + runID + "] cleanup out: ", logger: logger, sink: &combined}
	command.Stderr = &lineLogger{prefix: "[deploy:" + runID + "] cleanup err: ", logger: logger, sink: &combined}

	err = command.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("timed out after %s", postDeployCleanupTimeout)
	}
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(combined.String(), "\n") {
		if summary, ok := strings.CutPrefix(line, "cleanup "); ok {
			return summary, nil
		}
	}
	return "done", nil
}
