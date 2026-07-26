package singleserver

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestKamalDeployScriptDetectsAndCopiesRepoHooks(t *testing.T) {
	dir := t.TempDir()
	repoDir := filepath.Join(dir, "repo")
	fakeBin := filepath.Join(dir, "bin")
	kamalArgsFile := filepath.Join(dir, "kamal-args")

	if err := os.MkdirAll(filepath.Join(repoDir, ".git", "info"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, ".git", "info", "exclude"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	hooksDir := filepath.Join(repoDir, ".kamal", "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooksDir, "pre-deploy"), []byte("#!/bin/sh\nexit 0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooksDir, "post-deploy"), []byte("#!/bin/sh\nexit 0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fakeBin, 0755); err != nil {
		t.Fatal(err)
	}
	writeFakeCommand(t, fakeBin, "git", `#!/bin/sh
case "$1" in
  remote|fetch|reset|clean) exit 0 ;;
  ls-files) exit 1 ;;
esac
echo "unexpected git command: $*" >&2
exit 1
`)
	writeFakeCommand(t, fakeBin, "docker", `#!/bin/sh
if [ "$1" = "ps" ]; then
  exit 0
fi
echo "unexpected docker command: $*" >&2
exit 1
`)
	writeFakeCommand(t, fakeBin, "kamal", `#!/bin/sh
printf '%s\n' "$*" > "$KAMAL_ARGS_FILE"
test -x .singleserver-generated/hooks/pre-deploy
test -x .singleserver-generated/hooks/post-deploy
test ! -x .kamal/hooks/pre-deploy
test ! -x .kamal/hooks/post-deploy
`)

	cmd := exec.Command("bash", "-c", kamalDeployScript)
	cmd.Env = append(os.Environ(),
		"PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"KAMAL_ARGS_FILE="+kamalArgsFile,
		"SINGLESERVER_APP_NAME=scoreboard",
		"SINGLESERVER_REPO_DIR="+repoDir,
		"SINGLESERVER_REPO=acme/scoreboard",
		"SINGLESERVER_SHA=abc123",
		"SINGLESERVER_GITHUB_TOKEN=token",
		"SINGLESERVER_GENERATED_DEPLOY_YML=service: scoreboard\n",
		"SINGLESERVER_GENERATED_DOCKERFILE=FROM scratch\n",
		"SINGLESERVER_GENERATED_DOCKERFILE_SOURCE=generated",
		"SINGLESERVER_ENV_FILE="+filepath.Join(dir, "missing-env"),
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("deploy script failed: %v\n%s", err, output)
	}

	if !strings.Contains(string(output), "hooks=pre-deploy,post-deploy") {
		t.Fatalf("expected detected hooks in output:\n%s", output)
	}
	args, err := os.ReadFile(kamalArgsFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(args)) != "setup -q" {
		t.Fatalf("unexpected kamal args: %q", string(args))
	}
}

func writeFakeCommand(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
}
