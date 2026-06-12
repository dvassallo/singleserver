package singleserver

import (
	"errors"
	"io"
	"time"
)

func cliUpgrade(args []string, w io.Writer) error {
	mode, args, err := commandModeFromArgs(args, noFlagValues)
	if err != nil {
		return err
	}
	if len(args) != 0 {
		return errors.New("usage: singleserver upgrade")
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
	if err := commandRunFunc(10*time.Minute, "bash", "-lc", "curl -fsSL https://singleserver.com/install.sh | sh"); err != nil {
		return err
	}
	if err := commandRunFunc(15*time.Second, "systemctl", "restart", "singleserver.service"); err != nil {
		return err
	}
	writeCheck(w, "upgrade", "installer", "ok", "completed")
	return cliDoctor(nil, w)
}
