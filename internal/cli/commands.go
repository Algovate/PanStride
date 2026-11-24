package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/pflag"

	"panstride/internal/baidu"
	"panstride/internal/transfer"
)

func runLogin(args []string, out io.Writer) error {
	if len(args) != 0 {
		return &usageError{command: "login", message: "login does not accept a path or other arguments"}
	}
	if err := baidu.Login(out); err != nil {
		return err
	}
	fmt.Fprintln(out, "Signed in. Authorization saved.")
	return nil
}

func runList(ctx context.Context, args []string, format listingFormat, out io.Writer) error {
	remote := "/"
	switch len(args) {
	case 0:
	case 1:
		remote = args[0]
	default:
		return &usageError{command: "ls", message: "ls accepts at most one remote directory"}
	}
	remote, err := remotePath(remote)
	if err != nil {
		return &usageError{command: "ls", message: err.Error()}
	}

	drive, err := baidu.Open()
	if err != nil {
		return err
	}
	return writeListing(out, format, func(visit baidu.VisitEntry) error {
		return drive.List(ctx, remote, visit)
	})
}

func runSearch(ctx context.Context, args []string, asJSON bool, out io.Writer) error {
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		return &usageError{command: "search", message: "search requires exactly one non-empty keyword"}
	}
	drive, err := baidu.Open()
	if err != nil {
		return err
	}
	return writeListing(out, listingFormat{asJSON: asJSON, showIDs: true}, func(visit baidu.VisitEntry) error {
		return drive.Search(ctx, strings.TrimSpace(args[0]), visit)
	})
}

func runWhoami(ctx context.Context, args []string, out io.Writer) error {
	if len(args) != 0 {
		return &usageError{command: "whoami", message: "whoami does not accept arguments"}
	}
	drive, err := baidu.Open()
	if err != nil {
		return err
	}
	user, err := drive.Account(ctx)
	if err != nil {
		return err
	}
	name := user.NetdiskName
	if name == "" {
		name = user.BaiduName
	}
	source, expiresAt := drive.TokenInfo()
	fmt.Fprintf(out, "Account: %q (ID %d)\nToken source: %s\n", name, user.UK, source)
	if !expiresAt.IsZero() {
		fmt.Fprintln(out, "Expires:", expiresAt.Format(time.RFC3339))
	}
	return nil
}

const (
	defaultStallTimeout   = 45 * time.Second
	automaticStallMinSize = 64 << 20
)

type downloadOptions struct {
	output          string
	connections     int
	id              int64
	byID            bool
	keepGoing       bool
	stallTimeout    time.Duration
	stallTimeoutSet bool
}

func (o downloadOptions) stallTimeoutFor(size int64) time.Duration {
	if !o.stallTimeoutSet && size < automaticStallMinSize {
		return 0
	}
	return o.stallTimeout
}

func downloadFlags() (*pflag.FlagSet, *downloadOptions) {
	flags := pflag.NewFlagSet("download", pflag.ContinueOnError)
	options := new(downloadOptions)
	flags.StringVarP(&options.output, "output", "o", ".", "local destination directory")
	flags.IntVarP(&options.connections, "connections", "c", 8, "maximum connections per file")
	flags.Int64Var(&options.id, "id", 0, "Baidu file or folder ID from ls --ids or search")
	flags.BoolVar(&options.keepGoing, "keep-going", false, "continue folder downloads after failures")
	flags.DurationVar(&options.stallTimeout, "stall-timeout", defaultStallTimeout, "retry once after no new data for this duration (0 disables)")
	return flags, options
}

// The previous parser accepted single-dash long options. Leave option values untouched.
func normalizeDownloadFlags(args []string) []string {
	args = slices.Clone(args)
	for i := 0; i < len(args) && args[i] != "--"; i++ {
		name, _, hasValue := strings.Cut(args[i], "=")
		switch name {
		case "-output", "-connections":
			args[i] = "-" + args[i]
			fallthrough
		case "-o", "-c", "--output", "--connections", "--id", "--stall-timeout":
			if !hasValue {
				i++
			}
		}
	}
	return args
}

func runDownload(ctx context.Context, args []string, options downloadOptions, out io.Writer) error {
	switch {
	case options.byID && len(args) != 0:
		return &usageError{command: "download", message: "--id cannot be used with a remote path"}
	case options.byID && options.id <= 0:
		return &usageError{command: "download", message: "--id must be a positive file ID"}
	case !options.byID && len(args) != 1:
		return &usageError{command: "download", message: "download requires exactly one remote path or --id"}
	}
	if options.connections < 1 || options.connections > 16 {
		return &usageError{command: "download", message: "--connections must be between 1 and 16"}
	}
	if options.output == "" {
		return &usageError{command: "download", message: "--output cannot be empty"}
	}
	if options.stallTimeout < 0 {
		return &usageError{command: "download", message: "--stall-timeout must be zero or a positive duration"}
	}
	var remote string
	if !options.byID {
		var err error
		remote, err = remotePath(args[0])
		if err != nil {
			return &usageError{command: "download", message: err.Error()}
		}
	}

	dir, err := filepath.Abs(options.output)
	if err != nil {
		return err
	}
	aria, err := exec.LookPath("aria2c")
	if err != nil {
		return errors.New("install aria2c and make sure it is on PATH")
	}
	drive, err := baidu.Open()
	if err != nil {
		return err
	}
	job := downloadJob{drive: drive, out: out, keepGoing: options.keepGoing}
	job.transfer = func(link, target string, version transfer.Version) (bool, error) {
		return transfer.Download(ctx, aria, link, target, version, transfer.Options{
			Connections:  options.connections,
			StallTimeout: options.stallTimeoutFor(version.Size),
			UserAgent:    baidu.DownloadUserAgent,
			ContentMD5: func(ctx context.Context) (string, error) {
				return baidu.ContentMD5(ctx, link, version.Size)
			},
		}, out)
	}
	source := downloadSource{path: remote}
	if options.byID {
		source.id = options.id
	}
	return job.run(ctx, source, dir)
}

func remotePath(raw string) (string, error) {
	if !strings.HasPrefix(raw, "/") || strings.ContainsRune(raw, 0) {
		return "", errors.New("remote paths must start with /")
	}
	return path.Clean(raw), nil
}
