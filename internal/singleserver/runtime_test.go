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
