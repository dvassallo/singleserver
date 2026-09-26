package singleserver

import (
	"bufio"
	"io"
)

// A fresh bufio.Reader per prompter would swallow input the previous one buffered ahead.
var sharedPromptReader struct {
	src io.Reader
	r   *bufio.Reader
}

func promptReaderFor(input io.Reader) *bufio.Reader {
	if sharedPromptReader.src != input {
		sharedPromptReader.src = input
		sharedPromptReader.r = bufio.NewReader(input)
	}
	return sharedPromptReader.r
}

func interactivePrompter(w io.Writer) addPrompter {
	return addPrompter{reader: promptReaderFor(addPromptInput), w: w}
}
