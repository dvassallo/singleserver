package singleserver

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestMatchingAppContainerNames(t *testing.T) {
	output := strings.Join([]string{
		"scoreboard-web-123",
		"scoreboard",
		"scoreboarder-web-456",
		"other",
		"",
	}, "\n")

	got := matchingAppContainerNames("scoreboard", output)
	want := []string{"scoreboard-web-123", "scoreboard"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("matchingAppContainerNames() = %#v, want %#v", got, want)
	}
}

func TestRunningAppContainersParsesDockerOutput(t *testing.T) {
	original := commandOutputFunc
	t.Cleanup(func() { commandOutputFunc = original })
	commandOutputFunc = func(timeout time.Duration, name string, args ...string) (string, error) {
		if name != "docker" || strings.Join(args, " ") != "ps --format {{.Names}}" {
			t.Fatalf("unexpected command: %s %s", name, strings.Join(args, " "))
		}
		return "scoreboard-web-123\n\nother\n", nil
	}

	containers, err := runningAppContainers()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"scoreboard-web-123": "scoreboard-web-123",
		"other":              "other",
	}
	if !reflect.DeepEqual(containers, want) {
		t.Fatalf("runningAppContainers() = %#v, want %#v", containers, want)
	}
}

func TestStartContainersSkipsEmptyList(t *testing.T) {
	original := commandRunFunc
	t.Cleanup(func() { commandRunFunc = original })
	commandRunFunc = func(timeout time.Duration, name string, args ...string) error {
		t.Fatalf("did not expect command to run: %s %s", name, strings.Join(args, " "))
		return nil
	}

	if err := startContainers(nil); err != nil {
		t.Fatal(err)
	}
}

func TestDeployedCommitFromContainer(t *testing.T) {
	cases := []struct {
		name      string
		container string
		want      string
	}{
		{"kamal name carries the sha", "cadim-web-ac8ddaf778db9a16eee02b1f727906419a46af44", "ac8ddaf778db9a16eee02b1f727906419a46af44"},
		{"short sha", "cadim-web-ac8ddaf", "ac8ddaf"},
		{"non-sha version is not a commit", "cadim-web-v2", ""},
		{"hex-looking but too short", "cadim-web-abc", ""},
		{"uppercase is not a git sha", "cadim-web-AC8DDAF", ""},
		{"no version segment", "cadim", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deployedCommitFromContainer(tc.container); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDeployedCommitForAppIsEmptyWhenStopped(t *testing.T) {
	containers := map[string]string{"other-web-ac8ddaf": "other-web-ac8ddaf"}
	if got := deployedCommitForApp("cadim", containers); got != "" {
		t.Fatalf("a stopped app has no deployed commit, got %q", got)
	}
	containers["cadim-web-ac8ddaf"] = "cadim-web-ac8ddaf"
	if got := deployedCommitForApp("cadim", containers); got != "ac8ddaf" {
		t.Fatalf("got %q, want %q", got, "ac8ddaf")
	}
}
