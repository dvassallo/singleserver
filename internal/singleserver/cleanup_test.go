package singleserver

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGeneratedDeployYAMLRetainContainersOnlyWithCleanup(t *testing.T) {
	off := AppConfig{Repo: "acme/web"}
	body, err := GeneratedDeployYAML(off)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "retain_containers") {
		t.Fatalf("cleanup off must keep Kamal's default retention:\n%s", body)
	}

	on := AppConfig{Repo: "acme/web", CleanupAfterDeploy: true}
	body, err = GeneratedDeployYAML(on)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "\nretain_containers: 2\n") {
		t.Fatalf("expected retain_containers: 2:\n%s", body)
	}

	custom := AppConfig{Repo: "acme/web", CleanupAfterDeploy: true, RetainContainers: 3}
	body, err = GeneratedDeployYAML(custom)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "\nretain_containers: 3\n") {
		t.Fatalf("expected retain_containers: 3:\n%s", body)
	}
}

func TestCleanupConfigRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "apps.yml")
	body := "apps:\n  - repo: acme/web\n    cleanup_after_deploy: true\n  - repo: acme/api\n    cleanup_after_deploy: true\n    retain_containers: 4\n  - acme/site\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !config.Apps[0].CleanupAfterDeploy || config.Apps[0].RetainContainers != 2 {
		t.Fatalf("web: %+v", config.Apps[0])
	}
	if config.Apps[1].RetainContainers != 4 {
		t.Fatalf("api: %+v", config.Apps[1])
	}
	if config.Apps[2].CleanupAfterDeploy || config.Apps[2].RetainContainers != 0 {
		t.Fatalf("site must default to no cleanup: %+v", config.Apps[2])
	}

	if err := writeConfig(path, config); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "apps:\n  - repo: acme/web\n    cleanup_after_deploy: true\n  - repo: acme/api\n    cleanup_after_deploy: true\n    retain_containers: 4\n  - acme/site\n"
	if string(written) != want {
		t.Fatalf("written config:\n%s\nwant:\n%s", written, want)
	}
}

func TestCleanupConfigRejectsNegativeRetain(t *testing.T) {
	app := AppConfig{Repo: "acme/web", CleanupAfterDeploy: true, RetainContainers: -1}
	if err := app.Normalize(); err == nil {
		t.Fatal("expected error for negative retain_containers")
	}
}

func TestCleanupScriptNeverTouchesVolumesOrRegistry(t *testing.T) {
	script := strings.ToLower(postDeployCleanupScript)
	for _, line := range strings.Split(script, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		for _, forbidden := range []string{"volume", "system prune", "registry", "/srv/storage", "rm -rf"} {
			if strings.Contains(trimmed, forbidden) {
				t.Fatalf("cleanup script line %q contains %q", line, forbidden)
			}
		}
	}
}

// fakeCleanupEnv puts fake kamal and docker binaries on PATH that record each
// call, and returns the app plus the path of the call log.
func fakeCleanupEnv(t *testing.T, kamalExit int) (AppConfig, string) {
	t.Helper()
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	repoDir := filepath.Join(root, "repo")
	callLog := filepath.Join(root, "calls.log")
	for _, dir := range []string{binDir, repoDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	kamal := `#!/bin/sh
echo "kamal $*" >> "` + callLog + `"
cp config/deploy.yml "` + root + `/deploy.yml.seen" 2>/dev/null || true
exit ` + map[bool]string{true: "1", false: "0"}[kamalExit != 0] + "\n"
	docker := `#!/bin/sh
echo "docker $*" >> "` + callLog + `"
case "$1 $2" in
  "builder prune") printf 'ID\tRECLAIMABLE\tSIZE\nabc\ttrue\t1GB\nTotal:\t1.5GB\n' ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "kamal"), []byte(kamal), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "docker"), []byte(docker), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SINGLESERVER_STATE_DIR", filepath.Join(root, "state"))

	previousShell := postDeployCleanupShell
	postDeployCleanupShell = []string{"bash", "-c"}
	t.Cleanup(func() { postDeployCleanupShell = previousShell })

	app := AppConfig{Repo: "acme/web", RepoDir: repoDir, CleanupAfterDeploy: true}
	if err := app.Normalize(); err != nil {
		t.Fatal(err)
	}
	return app, callLog
}

func TestPostDeployCleanupRunsKamalPruneThenBuilderPrune(t *testing.T) {
	app, callLog := fakeCleanupEnv(t, 0)
	var logs bytes.Buffer
	m := &DeployManager{logger: log.New(&logs, "", 0)}

	m.cleanupAfterDeploy(app, "run-1")

	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatal(err)
	}
	var mutating []string
	for _, call := range strings.Split(strings.TrimSpace(string(calls)), "\n") {
		if strings.Contains(call, "volume") {
			t.Fatalf("cleanup ran a volume command: %q", call)
		}
		if strings.HasPrefix(call, "kamal ") || strings.Contains(call, " prune") {
			mutating = append(mutating, call)
		}
	}
	want := []string{"kamal prune all -q", "docker builder prune -f --filter until=24h"}
	if strings.Join(mutating, "\n") != strings.Join(want, "\n") {
		t.Fatalf("mutating calls:\n%s\nwant:\n%s", strings.Join(mutating, "\n"), strings.Join(want, "\n"))
	}

	seen, err := os.ReadFile(filepath.Join(filepath.Dir(callLog), "deploy.yml.seen"))
	if err != nil {
		t.Fatalf("kamal prune did not see a generated config/deploy.yml: %v", err)
	}
	if !strings.Contains(string(seen), "retain_containers: 2") {
		t.Fatalf("generated config for prune lacks retain_containers:\n%s", seen)
	}
	if _, err := os.Stat(filepath.Join(app.RepoDir, "config", "deploy.yml")); !os.IsNotExist(err) {
		t.Fatalf("generated config/deploy.yml should be removed after cleanup, stat err=%v", err)
	}

	if !strings.Contains(logs.String(), "[deploy:run-1] cleanup: containers_removed=0 images_removed=0 builder_reclaimed=1.5GB") {
		t.Fatalf("missing cleanup summary log:\n%s", logs.String())
	}
}

func TestPostDeployCleanupFailureIsOnlyLogged(t *testing.T) {
	app, callLog := fakeCleanupEnv(t, 1)
	var logs bytes.Buffer
	m := &DeployManager{logger: log.New(&logs, "", 0)}

	// cleanupAfterDeploy has no return value: a failing cleanup cannot turn a
	// successful deploy into a failed one. It must log and stop.
	m.cleanupAfterDeploy(app, "run-2")

	if !strings.Contains(logs.String(), "[deploy:run-2] cleanup failed") {
		t.Fatalf("missing cleanup failure log:\n%s", logs.String())
	}
	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(calls), "builder prune") {
		t.Fatalf("builder prune should not run after kamal prune failed:\n%s", calls)
	}
}
