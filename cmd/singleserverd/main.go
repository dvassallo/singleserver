package main

import (
	"log"
	"os"

	"github.com/dvassallo/singleserver/internal/singleserver"
)

func main() {
	logger := log.New(os.Stdout, "", log.LstdFlags|log.Lmicroseconds|log.LUTC)
	if err := singleserver.RunCLI(os.Args[1:], logger); err != nil {
		logger.Fatal(err)
	}
}
