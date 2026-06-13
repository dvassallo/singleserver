package main

import (
	"log"
	"os"
	"path/filepath"

	"github.com/dvassallo/singleserver/internal/singleserver"
)

func main() {
	logger := log.New(os.Stdout, "", log.LstdFlags|log.Lmicroseconds|log.LUTC)

	// Start the deploy daemon only when invoked as singleserverd with no
	// arguments, which is how systemd launches it. Every other invocation is
	// the CLI, so a bare `singleserver` prints usage instead of trying to start
	// a second daemon on the port the running one already holds.
	if len(os.Args) <= 1 && filepath.Base(os.Args[0]) == "singleserverd" {
		if err := singleserver.Run(logger); err != nil {
			logger.Fatal(err)
		}
		return
	}

	if err := singleserver.RunCLI(os.Args[1:], logger); err != nil {
		logger.Fatal(err)
	}
}
