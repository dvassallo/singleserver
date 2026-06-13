package singleserver

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

// useColor gates ANSI styling. It stays false unless RunCLI enables it for a
// real terminal, so captured output (tests, pipes) is always plain text.
var useColor bool

func enableColorForStdout() {
	useColor = shouldColor(os.Stdout, os.Getenv("NO_COLOR"), os.Getenv("TERM"))
}

func shouldColor(f *os.File, noColor, term string) bool {
	if strings.TrimSpace(noColor) != "" {
		return false
	}
	if term == "dumb" {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

const (
	ansiReset  = "\x1b[0m"
	ansiDim    = "\x1b[2m"
	ansiBold   = "\x1b[1m"
	ansiRed    = "\x1b[31m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
)

func paint(code, s string) string {
	if !useColor || s == "" {
		return s
	}
	return code + s + ansiReset
}

func dim(s string) string    { return paint(ansiDim, s) }
func bold(s string) string   { return paint(ansiBold, s) }
func green(s string) string  { return paint(ansiGreen, s) }
func red(s string) string    { return paint(ansiRed, s) }
func yellow(s string) string { return paint(ansiYellow, s) }

// stateKind drives the glyph and color for a status indicator.
type stateKind int

const (
	stateMuted stateKind = iota
	stateOK
	stateWarn
	stateFail
)

func stateColor(k stateKind) string {
	switch k {
	case stateOK:
		return ansiGreen
	case stateWarn:
		return ansiYellow
	case stateFail:
		return ansiRed
	default:
		return ansiDim
	}
}

// dot is the round status indicator used in headers and tables.
func dot(k stateKind) string {
	if k == stateMuted {
		return dim("○")
	}
	return paint(stateColor(k), "●")
}

// mark is the inline indicator used in detail lines.
func mark(k stateKind) string {
	switch k {
	case stateOK:
		return green("✓")
	case stateWarn:
		return yellow("!")
	case stateFail:
		return red("✗")
	default:
		return dim("–")
	}
}

// tcell carries the plain text used for width math and the styled text printed.
type tcell struct {
	text   string
	styled string
}

func cell(text, styled string) tcell { return tcell{text: text, styled: styled} }
func plainCell(text string) tcell    { return tcell{text: text, styled: text} }

func (c tcell) width() int { return utf8.RuneCountInString(c.text) }

// writeTable prints rows in aligned columns, measuring width from plain text so
// embedded ANSI codes never throw off the spacing. The final column is not
// padded, so lines carry no trailing whitespace.
func writeTable(w io.Writer, rows [][]tcell, gutter int) {
	if len(rows) == 0 {
		return
	}
	cols := 0
	for _, r := range rows {
		cols = max(cols, len(r))
	}
	widths := make([]int, cols)
	for _, r := range rows {
		for i, c := range r {
			widths[i] = max(widths[i], c.width())
		}
	}
	for _, r := range rows {
		var b strings.Builder
		for i, c := range r {
			b.WriteString(c.styled)
			if i < len(r)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-c.width()+gutter))
			}
		}
		fmt.Fprintln(w, b.String())
	}
}

func trimScheme(url string) string {
	url = strings.TrimPrefix(url, "https://")
	url = strings.TrimPrefix(url, "http://")
	return strings.TrimSuffix(url, "/")
}
