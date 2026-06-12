package singleserver

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
)

func cliBackup(args []string, w io.Writer) error {
	mode, args, err := commandModeFromArgs(args, noFlagValues)
	if err != nil {
		return err
	}
	if len(args) == 0 && cliCanPrompt(mode) {
		appName, err := promptConfiguredAppName(interactivePrompter(w), "App to back up")
		if err != nil {
			return err
		}
		args = append(args, appName)
	}
	if len(args) != 1 {
		return errors.New("usage: singleserver backup <app>")
	}
	app, err := configuredApp(args[0])
	if err != nil {
		return err
	}
	storage, err := requireStorage(app)
	if err != nil {
		return err
	}
	result, err := createStorageBackup(app.Name, storage.Path, filepath.Join(backupRoot(), app.Name))
	if err != nil {
		return err
	}
	writeCheck(w, app.Name, "backup", "ok", result.Path, fmt.Sprintf("files=%d", result.Files), fmt.Sprintf("sqlite=%d", result.SQLiteFiles))
	return nil
}

func cliRestore(args []string, w io.Writer) error {
	mode, args, err := commandModeFromArgs(args, noFlagValues)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	fs.SetOutput(w)
	noRestart := fs.Bool("no-restart", false, "restore files without restarting app containers")
	if err := fs.Parse(normalizeFlagArgs(args, noFlagValues)); err != nil {
		return err
	}
	prompting := cliCanPrompt(mode)
	p := interactivePrompter(w)
	restoreArgs := fs.Args()
	if len(restoreArgs) == 0 && prompting {
		appName, err := promptConfiguredAppName(p, "App to restore")
		if err != nil {
			return err
		}
		restoreArgs = append(restoreArgs, appName)
	}
	if len(restoreArgs) == 1 && prompting {
		backup, err := p.askRequired("Backup id or path")
		if err != nil {
			return err
		}
		restoreArgs = append(restoreArgs, backup)
	}
	if len(restoreArgs) != 2 {
		return errors.New("usage: singleserver restore <app> <backup-id-or-path> [--no-restart]")
	}
	app, err := configuredApp(restoreArgs[0])
	if err != nil {
		return err
	}
	storage, err := requireStorage(app)
	if err != nil {
		return err
	}
	backupPath := resolveBackupPath(app.Name, restoreArgs[1])
	if prompting {
		fmt.Fprintf(w, "Restore %s from %s.\n", app.Name, backupPath)
		fmt.Fprintf(w, "Current storage will move aside before the backup is restored: %s\n", storage.Path)
		proceed, err := p.askYesNo("Continue?", false)
		if err != nil {
			return err
		}
		if !proceed {
			writeCheck(w, app.Name, "restore", "canceled", backupPath)
			return nil
		}
	}
	if err := restoreStorageBackup(app.Name, storage.Path, backupPath, *noRestart, w); err != nil {
		return err
	}
	return nil
}
