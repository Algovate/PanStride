package transfer

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"
)

type digestProbe func(context.Context) (string, error)

type Options struct {
	Connections  int
	StallTimeout time.Duration
	UserAgent    string
	ContentMD5   func(context.Context) (string, error)
}

func Download(ctx context.Context, aria, link, target string, version Version, options Options, out io.Writer) (bool, error) {
	return storeFile(ctx, target, version, out, func(part string, resume bool) error {
		if resume {
			fmt.Fprintf(out, "Resuming %q (up to %d connections)\n", target, options.Connections)
		} else {
			fmt.Fprintf(out, "Downloading %q (up to %d connections)\n", target, options.Connections)
		}

		attemptCtx, cancel := context.WithCancelCause(ctx)
		defer cancel(nil)
		configArg := "--no-conf"
		input := link + "\n  out=" + filepath.Base(part) + "\n"
		var control *ariaControl
		if options.StallTimeout > 0 {
			var err error
			control, err = newAriaControl(filepath.Dir(part))
			if err != nil {
				return fmt.Errorf("start aria2 transfer monitor: %w", err)
			}
			defer control.close()
			configArg = "--conf-path=" + control.config
			input += "  gid=" + ariaDownloadID + "\n"
		}
		progress := transferProgress{out: out, total: version.Size}
		if file, ok := out.(*os.File); ok {
			progress.inline = term.IsTerminal(int(file.Fd()))
		}
		count := strconv.Itoa(options.Connections)
		cmd := exec.CommandContext(attemptCtx, aria,
			configArg, "--input-file=-", "--enable-color=false",
			"--show-console-readout=true", "--summary-interval=0", "--download-result=hide",
			"--console-log-level=error", "--stderr=true",
			"--continue=true", "--auto-file-renaming=false", "--allow-overwrite=false",
			"--file-allocation=none", "--check-certificate=true",
			"--max-connection-per-server="+count, "--split="+count, "--min-split-size=1M",
			"--max-tries=5", "--retry-wait=2", "--user-agent="+options.UserAgent,
			"--dir="+filepath.Dir(part))
		// Keep signed URLs off the command line and discard aria2's free-form logs.
		cmd.Stdin = strings.NewReader(input)
		// aria2 buffers progress on a stdout pipe. Its stderr stream is unbuffered.
		cmd.Stderr = &progress
		cmd.Cancel = func() error {
			err := cmd.Process.Signal(os.Interrupt)
			if err == nil || control == nil {
				return err
			}
			return control.shutdown(context.Background())
		}
		cmd.WaitDelay = 8 * time.Second
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("start aria2c for %q: %w", target, err)
		}
		if control != nil {
			go control.watch(attemptCtx, options.StallTimeout, cancel)
		}
		err := cmd.Wait()
		cause := context.Cause(attemptCtx)
		cancel(nil)
		progress.finish()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if cause != nil {
			return cause
		}
		if err != nil {
			return fmt.Errorf("aria2c failed for %q: %w", target, err)
		}
		return nil
	}, options.ContentMD5)
}
