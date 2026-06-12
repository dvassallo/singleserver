package singleserver

import (
	"errors"
	"os"
	"strings"
)

type cliMode struct {
	NonInteractive bool
}

var activeCLIMode cliMode

func currentCLIMode() cliMode {
	mode := cliMode{
		NonInteractive: truthyEnv("SINGLESERVER_NON_INTERACTIVE"),
	}
	if activeCLIMode.NonInteractive {
		mode.NonInteractive = true
	}
	return mode
}

func truthyEnv(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func parseRootCLIMode(args []string) (cliMode, []string, error) {
	mode := currentCLIMode()
	for len(args) > 0 {
		switch args[0] {
		case "--non-interactive":
			mode.NonInteractive = true
			args = args[1:]
		case "--yes":
			return mode, nil, removedYesFlagError()
		default:
			return mode, args, nil
		}
	}
	return mode, args, nil
}

func withCLIMode(mode cliMode, run func() error) error {
	previous := activeCLIMode
	activeCLIMode = mode
	defer func() {
		activeCLIMode = previous
	}()
	return run()
}

func commandModeFromArgs(args []string, takesValue func(string) bool) (cliMode, []string, error) {
	mode := currentCLIMode()
	stripped := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			stripped = append(stripped, args[i:]...)
			break
		}
		switch arg {
		case "--non-interactive":
			mode.NonInteractive = true
			continue
		case "--yes":
			return mode, nil, removedYesFlagError()
		}
		stripped = append(stripped, arg)
		if strings.HasPrefix(arg, "-") && arg != "-" && takesValue(arg) && !strings.Contains(arg, "=") && i+1 < len(args) {
			i++
			stripped = append(stripped, args[i])
		}
	}
	return mode, stripped, nil
}

func removedYesFlagError() error {
	return errors.New("--yes has been removed; use --non-interactive when you want a command to run without prompts")
}

func cliCanPrompt(mode cliMode) bool {
	return !mode.NonInteractive && addPromptInteractiveFunc()
}

func normalizeFlagArgs(args []string, takesValue func(string) bool) []string {
	flags := []string{}
	positionals := []string{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if strings.HasPrefix(arg, "-") && arg != "-" {
			flags = append(flags, arg)
			if takesValue(arg) && !strings.Contains(arg, "=") && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		positionals = append(positionals, arg)
	}
	return append(flags, positionals...)
}

func noFlagValues(string) bool {
	return false
}
