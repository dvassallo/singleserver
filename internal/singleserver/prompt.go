package singleserver

import (
	"bufio"
	"io"
)

func interactivePrompter(w io.Writer) addPrompter {
	return addPrompter{reader: bufio.NewReader(addPromptInput), w: w}
}
