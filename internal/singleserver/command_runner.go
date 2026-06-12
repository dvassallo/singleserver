package singleserver

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"time"
)

func runCommandToWriter(w io.Writer, timeout time.Duration, name string, args ...string) error {
	ctx := context.Background()
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = w
	cmd.Stderr = w
	err := cmd.Run()
	if timeout > 0 && ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("%s timed out", name)
	}
	return err
}

var commandRunToWriterFunc = runCommandToWriter
