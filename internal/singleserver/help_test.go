package singleserver

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestHelpRequestParsesPath(t *testing.T) {
	tests := []struct {
		name string
		args []string
		path []string
		ok   bool
	}{
		{"bare help", []string{"help"}, []string{}, true},
		{"help with path", []string{"help", "storage", "enable"}, []string{"storage", "enable"}, true},
		{"trailing --help", []string{"storage", "enable", "--help"}, []string{"storage", "enable"}, true},
		{"short -h alone", []string{"-h"}, []string{}, true},
		{"flag before --help", []string{"add", "--no-deploy", "--help"}, []string{"add"}, true},
		{"no help", []string{"deploy", "myapp"}, nil, false},
		{"help after double dash is not help", []string{"storage", "enable", "--", "--help"}, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path, ok := helpRequest(tt.args)
			if ok != tt.ok {
				t.Fatalf("helpRequest(%v) ok = %v, want %v", tt.args, ok, tt.ok)
			}
			if ok && !reflect.DeepEqual(path, tt.path) {
				t.Fatalf("helpRequest(%v) path = %#v, want %#v", tt.args, path, tt.path)
			}
		})
	}
}

func TestHelpRendersLeafCommandOptions(t *testing.T) {
	var out bytes.Buffer
	if err := renderHelp(&out, []string{"storage", "enable"}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"singleserver storage enable <app> [options]",
		"Arguments:",
		"<app>",
		"Options:",
		"--mount <path>",
		"--path <path>",
		"--no-deploy",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected leaf help to contain %q, got:\n%s", want, got)
		}
	}
}

func TestHelpListsSubcommands(t *testing.T) {
	var out bytes.Buffer
	if err := renderHelp(&out, []string{"storage"}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"Subcommands:", "enable", "disable", "singleserver help storage <subcommand>"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected parent help to contain %q, got:\n%s", want, got)
		}
	}
}

func TestHelpUnknownCommandFallsBackToOverview(t *testing.T) {
	var out bytes.Buffer
	if err := renderHelp(&out, []string{"bogus"}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, `Unknown command "bogus"`) {
		t.Fatalf("expected unknown-command note, got:\n%s", got)
	}
	if !strings.Contains(got, "Run `singleserver help <command>`") {
		t.Fatalf("expected overview fallback, got:\n%s", got)
	}
}

// TestCommandRegistryWellFormed guards the invariants the help and dispatch code
// rely on, so a new command can't be half-registered.
func TestCommandRegistryWellFormed(t *testing.T) {
	groups := map[string]bool{}
	for _, g := range commandGroups {
		groups[g] = true
	}
	for _, c := range cliCommands {
		if !groups[c.Group] {
			t.Errorf("command %q has unknown group %q", c.Name, c.Group)
		}
		if strings.TrimSpace(c.Summary) == "" {
			t.Errorf("command %q has no summary", c.Name)
		}
		if c.Run == nil {
			t.Errorf("command %q has no Run", c.Name)
		}
		for _, ch := range c.Children {
			if strings.TrimSpace(ch.Summary) == "" {
				t.Errorf("subcommand %q of %q has no summary", ch.Name, c.Name)
			}
		}
	}
}
