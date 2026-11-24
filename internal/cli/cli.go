package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/pflag"
)

const usage = `PanStride — Baidu Netdisk CLI

Usage:
  panstride <command> [options]

Commands:
  login      Authorize PanStride in your browser.
  ls         List files and folders in a remote directory.
  search     Find files or folders by name.
  whoami     Show the account associated with the active token.
  download   Download a file or folder using aria2c.
  help       Show help for a command.

Run 'panstride help <command>' or 'panstride <command> --help' for details.
`

var commandHelp = map[string]string{
	"help": `Usage: panstride help [command]

Show general help or details for a command.

Options:
  -h, --help   Show this help.
`,
	"login": `Usage: panstride login

Open the authorization URL in your browser, authorize the application,
then paste the full success-page URL at the hidden prompt.

Options:
  -h, --help   Show this help.

If you already have an OAuth token, BAIDU_ACCESS_TOKEN takes precedence
over the saved login.
`,
	"ls": `Usage: panstride ls [options] [remote-directory]

List the root directory if no path is given. Remote paths start with /.
Each row contains the size in bytes (or "dir") and the remote path,
separated by a tab.

Options:
      --ids     Include the Baidu file ID as the first column.
      --json    Write a JSON array containing IDs, paths, sizes, and directory flags.
  -h, --help    Show this help.

Example:
  panstride ls --ids "/资料"
`,
	"search": `Usage: panstride search [options] <keyword>

Search accessible directories recursively. Each result includes its
Baidu file ID, size in bytes (or "dir"), and full remote path.

Options:
      --json    Write a JSON array containing IDs, paths, sizes, and directory flags.
  -h, --help    Show this help.

Example:
  panstride search "戏剧节"
`,
	"whoami": `Usage: panstride whoami

Show the current Baidu Netdisk account, token source, and expiration
when it is known.

Options:
  -h, --help   Show this help.
`,
	"download": `Usage:
  panstride download [options] <remote-path>
  panstride download [options] --id <file-id>

Download a file or recursively download a folder. Requires aria2c on PATH.
Remote paths start with /. Options can appear before or after the path.
To resume an interrupted download, rerun the same command.

Options:
  -o, --output <directory>    Local destination (default: current directory).
  -c, --connections <number>  Maximum connections per file, 1–16 (default: 8).
      --id <file-id>          Baidu file or folder ID from ls --ids or search.
      --keep-going            Continue folder downloads after an entry fails.
      --stall-timeout <time>  Retry once after no new data.
                              Default: 45s for files of at least 64 MiB. Use 0 to disable.
  -h, --help                  Show this help.

Example:
  panstride download "/资料/视频.mp4" -o ./downloads -c 8
`,
}

type usageError struct {
	command string
	message string
}

func (e *usageError) Error() string { return e.message }

func Main(args []string, out, errOut io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := run(ctx, args, out)
	if err == nil {
		return 0
	}
	fmt.Fprintln(errOut, "panstride:", err)
	var invalid *usageError
	switch {
	case errors.As(err, &invalid):
		help := "panstride --help"
		if invalid.command != "" {
			help = "panstride " + invalid.command + " --help"
		}
		fmt.Fprintf(errOut, "Run '%s' for usage.\n", help)
		return 2
	case errors.Is(err, context.Canceled):
		return 130
	default:
		return 1
	}
}

func run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(out, usage)
		return nil
	}
	name, args := args[0], args[1:]
	if name == "-h" || name == "--help" {
		return runHelp(args, out)
	}
	help, known := commandHelp[name]
	if !known {
		return &usageError{message: fmt.Sprintf("unknown command %q", name)}
	}

	flags := pflag.NewFlagSet(name, pflag.ContinueOnError)
	var options *downloadOptions
	var format listingFormat
	switch name {
	case "download":
		flags, options = downloadFlags()
		args = normalizeDownloadFlags(args)
	case "ls":
		flags.BoolVar(&format.showIDs, "ids", false, "include Baidu file IDs")
		fallthrough
	case "search":
		flags.BoolVar(&format.asJSON, "json", false, "write a JSON array")
	}
	flags.SetOutput(io.Discard)
	flags.Usage = func() { fmt.Fprint(out, help) }
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return nil
		}
		return &usageError{command: name, message: err.Error()}
	}

	switch name {
	case "help":
		return runHelp(flags.Args(), out)
	case "login":
		return runLogin(flags.Args(), out)
	case "ls":
		return runList(ctx, flags.Args(), format, out)
	case "search":
		return runSearch(ctx, flags.Args(), format.asJSON, out)
	case "whoami":
		return runWhoami(ctx, flags.Args(), out)
	case "download":
		options.byID = flags.Changed("id")
		options.stallTimeoutSet = flags.Changed("stall-timeout")
		return runDownload(ctx, flags.Args(), *options, out)
	}
	return &usageError{message: fmt.Sprintf("unknown command %q", name)}
}

func runHelp(args []string, out io.Writer) error {
	switch len(args) {
	case 0:
		fmt.Fprint(out, usage)
	case 1:
		help, known := commandHelp[args[0]]
		if !known {
			return &usageError{command: "help", message: fmt.Sprintf("unknown command %q", args[0])}
		}
		fmt.Fprint(out, help)
	default:
		return &usageError{command: "help", message: "help accepts at most one command"}
	}
	return nil
}
