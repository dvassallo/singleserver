package singleserver

import (
	"errors"
	"flag"
	"io"
	"time"
)

func cliUpgrade(args []string, w io.Writer) error {
	mode, args, err := commandModeFromArgs(args, noFlagValues)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	fs.SetOutput(w)
	edge := fs.Bool("edge", false, "install the latest edge build from main instead of the latest release")
	if err := fs.Parse(normalizeFlagArgs(args, noFlagValues)); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: singleserver upgrade [--edge]")
	}
	channel := "stable"
	if *edge {
		channel = "edge"
	}
	if cliCanPrompt(mode) {
		proceed, err := interactivePrompter(w).askYesNo("Upgrade Single Server now?", false)
		if err != nil {
			return err
		}
		if !proceed {
			writeCheck(w, "upgrade", "installer", "canceled", "-")
			return nil
		}
	}
	install := "curl -fsSL https://singleserver.com/install.sh | SINGLESERVER_CHANNEL=" + channel + " sh"
	if err := commandRunFunc(10*time.Minute, "bash", "-lc", install); err != nil {
		return err
	}
	if err := commandRunFunc(15*time.Second, "systemctl", "restart", "singleserver.service"); err != nil {
		return err
	}
	writeCheck(w, "upgrade", "installer", "ok", channel)
	return cliDoctor(nil, w)
}
