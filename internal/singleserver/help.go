package singleserver

import (
	"fmt"
	"io"
	"strings"
)

// helpRequest reports whether args ask for help and, if so, the command path to
// describe. `help [cmd [sub]]`, `-h`, and `--help` (anywhere before a `--`) all
// trigger it; the path is the leading bare words that name a command.
func helpRequest(args []string) ([]string, bool) {
	if len(args) > 0 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help") {
		return leadingWords(args[1:]), true
	}
	for _, a := range args {
		if a == "--" {
			break
		}
		if a == "-h" || a == "--help" {
			return leadingWords(args), true
		}
	}
	return nil, false
}

func leadingWords(args []string) []string {
	words := make([]string, 0, len(args))
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			break
		}
		words = append(words, a)
	}
	return words
}

// renderHelp writes help for the resolved command path. An empty path prints the
// top-level overview. Unknown leading words fall back to the overview so a typo
// still surfaces the command list.
func renderHelp(w io.Writer, path []string) error {
	if o, ok := w.(*Output); ok {
		o.helped = true
	}
	out := rawWriter(w)
	if len(path) == 0 {
		writeTopLevelHelp(out)
		return nil
	}
	cmd := lookupCommand(path[0])
	if cmd == nil {
		fmt.Fprintf(out, "Unknown command %q.\n\n", path[0])
		writeTopLevelHelp(out)
		return nil
	}
	resolved := []string{cmd.Name}
	for _, word := range path[1:] {
		ch := cmd.child(word)
		if ch == nil {
			break
		}
		cmd = ch
		resolved = append(resolved, cmd.Name)
	}
	writeCommandHelp(out, cmd, resolved)
	return nil
}

func printUsage(w io.Writer) {
	writeTopLevelHelp(rawWriter(w))
}

func writeTopLevelHelp(w io.Writer) {
	fmt.Fprint(w, "Single Server\n\n")
	fmt.Fprintf(w, "%s\n", bold("Usage:"))
	fmt.Fprint(w, "  singleserver [--non-interactive] [--output json] <command> [args]\n\n")
	fmt.Fprintf(w, "%s\n", bold("Global options:"))
	writeColumns(w, [][2]string{
		{"--non-interactive", "Never prompt. Missing required input is an error."},
		{"--output json", "Emit JSON instead of text."},
	}, 2, 3)
	for _, group := range commandGroups {
		rows := make([][2]string, 0)
		for _, c := range cliCommands {
			if c.Group == group {
				rows = append(rows, [2]string{c.Name, c.Summary})
			}
		}
		if len(rows) == 0 {
			continue
		}
		fmt.Fprintf(w, "\n%s\n", bold(group))
		writeColumns(w, rows, 2, 3)
	}
	fmt.Fprint(w, "\nRun `singleserver help <command>` for details on a command.\n")
}

func writeCommandHelp(w io.Writer, cmd *command, path []string) {
	full := strings.Join(path, " ")
	if cmd.Summary != "" {
		fmt.Fprintf(w, "%s\n\n", cmd.Summary)
	}
	fmt.Fprintf(w, "%s\n", bold("Usage:"))
	if len(cmd.Children) > 0 {
		for _, ch := range cmd.Children {
			fmt.Fprintf(w, "  %s\n", usageLine(full+" "+ch.Name, ch.Usage))
		}
	} else {
		fmt.Fprintf(w, "  %s\n", usageLine(full, cmd.Usage))
	}
	if strings.TrimSpace(cmd.Long) != "" {
		fmt.Fprintf(w, "\n%s\n", cmd.Long)
	}
	if len(cmd.Args) > 0 {
		fmt.Fprintf(w, "\n%s\n", bold("Arguments:"))
		writeColumns(w, argRows(cmd.Args), 2, 3)
	}
	if len(cmd.Flags) > 0 {
		fmt.Fprintf(w, "\n%s\n", bold("Options:"))
		writeColumns(w, flagRows(cmd.Flags), 2, 3)
	}
	if len(cmd.Children) > 0 {
		rows := make([][2]string, 0, len(cmd.Children))
		for _, ch := range cmd.Children {
			rows = append(rows, [2]string{ch.Name, ch.Summary})
		}
		fmt.Fprintf(w, "\n%s\n", bold("Subcommands:"))
		writeColumns(w, rows, 2, 3)
		fmt.Fprintf(w, "\nRun `singleserver help %s <subcommand>` for details.\n", full)
	} else {
		fmt.Fprint(w, "\nGlobal options: --non-interactive, --output json\n")
	}
}

func usageLine(path, usage string) string {
	return strings.TrimRight("singleserver "+path+" "+usage, " ")
}

func argRows(args []argSpec) [][2]string {
	rows := make([][2]string, 0, len(args))
	for _, a := range args {
		rows = append(rows, [2]string{a.Name, a.Desc})
	}
	return rows
}

func flagRows(flags []flagSpec) [][2]string {
	rows := make([][2]string, 0, len(flags))
	for _, f := range flags {
		rows = append(rows, [2]string{f.Name, f.Desc})
	}
	return rows
}

// writeColumns prints aligned two-column rows. The left column width is measured
// from the plain key text so alignment stays correct if a renderer adds color.
func writeColumns(w io.Writer, rows [][2]string, indent, gap int) {
	width := 0
	for _, r := range rows {
		width = max(width, len(r[0]))
	}
	prefix := strings.Repeat(" ", indent)
	for _, r := range rows {
		pad := strings.Repeat(" ", width-len(r[0])+gap)
		fmt.Fprintln(w, strings.TrimRight(prefix+r[0]+pad+r[1], " "))
	}
}
