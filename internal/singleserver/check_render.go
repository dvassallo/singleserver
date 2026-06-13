package singleserver

import (
	"bytes"
	"fmt"
	"io"
	"strings"
)

// checkRenderer turns writeCheck's tab-delimited lines into grouped, colored
// stanzas for terminals. It is used only when color is enabled; piped output
// keeps the flat tab format through tabwriter, so scripts, the E2E harness, and
// tests (which write to plain buffers) see exactly the same bytes as before.
type checkRenderer struct {
	w       io.Writer
	line    bytes.Buffer
	pending []checkRow
	started bool
}

type checkRow struct {
	scope  string
	check  string
	status string
	value  string
}

func newCheckRenderer(w io.Writer) *checkRenderer {
	return &checkRenderer{w: w}
}

func (r *checkRenderer) Write(p []byte) (int, error) {
	for _, b := range p {
		if b == '\n' {
			r.handleLine(r.line.String())
			r.line.Reset()
			continue
		}
		r.line.WriteByte(b)
	}
	return len(p), nil
}

func (r *checkRenderer) handleLine(line string) {
	if row, ok := parseCheckLine(line); ok {
		r.pending = append(r.pending, row)
		return
	}
	// Anything that is not a check line (data views, prompts, logs, YAML)
	// passes through verbatim, after first flushing any buffered checks so
	// ordering is preserved.
	r.flushPending()
	fmt.Fprintln(r.w, line)
}

// parseCheckLine recognizes a writeCheck line by its shape: at least four
// tab-separated fields with a known status word in the third. That keeps log
// output containing stray tabs from being mistaken for a check.
func parseCheckLine(line string) (checkRow, bool) {
	fields := strings.SplitN(line, "\t", 5)
	if len(fields) < 4 {
		return checkRow{}, false
	}
	if !isStatusWord(fields[2]) {
		return checkRow{}, false
	}
	value := fields[3]
	if value == "-" {
		value = ""
	}
	detail := ""
	if len(fields) == 5 {
		detail = fields[4]
	}
	return checkRow{
		scope:  fields[0],
		check:  fields[1],
		status: fields[2],
		value:  strings.TrimSpace(strings.Join(nonEmptyStrings(value, detail), " ")),
	}, true
}

func isStatusWord(s string) bool {
	switch s {
	case "ok", "failed", "pending", "skipped", "assumed", "kept", "previous", "canceled", "start", "starting":
		return true
	default:
		return false
	}
}

func (r *checkRenderer) flushPending() {
	for i := 0; i < len(r.pending); {
		j := i + 1
		for j < len(r.pending) && r.pending[j].scope == r.pending[i].scope {
			j++
		}
		r.renderGroup(r.pending[i:j])
		i = j
	}
	r.pending = r.pending[:0]
}

func (r *checkRenderer) renderGroup(group []checkRow) {
	if r.started {
		fmt.Fprintln(r.w)
	}
	r.started = true
	fmt.Fprintln(r.w, bold(group[0].scope))

	width := 0
	for _, c := range group {
		width = max(width, len(prettyCheck(c.check)))
	}
	for _, c := range group {
		name := prettyCheck(c.check)
		var b strings.Builder
		b.WriteString("  ")
		b.WriteString(mark(checkState(c.status)))
		b.WriteString(" ")
		b.WriteString(name)
		b.WriteString(strings.Repeat(" ", width-len(name)+3))
		// For ok/failed the glyph already carries the state; other statuses
		// (pending, skipped, kept, ...) keep their word so the meaning is clear.
		if c.status != "ok" && c.status != "failed" {
			b.WriteString(dim(c.status))
			if c.value != "" {
				b.WriteString(" ")
			}
		}
		b.WriteString(c.value)
		fmt.Fprintln(r.w, strings.TrimRight(b.String(), " "))
	}
}

func (r *checkRenderer) Flush() error {
	r.flushPending()
	if r.line.Len() > 0 {
		// A trailing partial line is almost always an interactive prompt; write
		// it as-is so no newline is inserted before the user's input.
		_, err := io.WriteString(r.w, r.line.String())
		r.line.Reset()
		return err
	}
	return nil
}

func prettyCheck(check string) string {
	return strings.ReplaceAll(check, "_", " ")
}

func checkState(status string) stateKind {
	switch status {
	case "ok":
		return stateOK
	case "failed":
		return stateFail
	case "pending", "canceled":
		return stateWarn
	default:
		return stateMuted
	}
}
