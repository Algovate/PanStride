package cli

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestHelpForEveryCommand(t *testing.T) {
	for _, command := range []string{"help", "login", "ls", "search", "whoami", "download"} {
		t.Run(command, func(t *testing.T) {
			var direct, routed strings.Builder
			if err := run(context.Background(), []string{command, "-h"}, &direct); err != nil {
				t.Fatal(err)
			}
			if err := run(context.Background(), []string{"help", command}, &routed); err != nil {
				t.Fatal(err)
			}
			if direct.String() != routed.String() || !strings.Contains(direct.String(), "panstride "+command) {
				t.Fatalf("inconsistent help: %q vs %q", direct.String(), routed.String())
			}
		})
	}
}

func TestCLIReportsSpecificUsageErrors(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"unknown"}, "unknown command"},
		{[]string{"help", "unknown"}, "unknown command"},
		{[]string{"login", "--unknown"}, "unknown flag"},
		{[]string{"login", "unexpected"}, "does not accept a path"},
		{[]string{"ls", "relative"}, "must start with /"},
		{[]string{"ls", "/", "/extra"}, "at most one"},
		{[]string{"search"}, "non-empty keyword"},
		{[]string{"whoami", "extra"}, "does not accept arguments"},
		{[]string{"download"}, "exactly one"},
		{[]string{"download", "--id", "0"}, "positive file ID"},
		{[]string{"download", "/file", "--stall-timeout=-1s"}, "zero or a positive duration"},
		{[]string{"download", "--id", "42", "/file"}, "cannot be used with a remote path"},
		{[]string{"download", "/one", "-c", "17"}, "between 1 and 16"},
		{[]string{"download", "-c", "0", "/one"}, "between 1 and 16"},
		{[]string{"download", "/one", "-output="}, "cannot be empty"},
	} {
		t.Run(strings.Join(test.args, " "), func(t *testing.T) {
			var out strings.Builder
			err := run(context.Background(), test.args, &out)
			var usage *usageError
			if !errors.As(err, &usage) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected a usage error containing %q, got %v", test.want, err)
			}
			if out.Len() != 0 {
				t.Fatalf("usage error wrote to stdout: %q", out.String())
			}
		})
	}
}

func TestDownloadFlagsCanSurroundPath(t *testing.T) {
	for _, args := range [][]string{
		{"-c", "12", "/资料/video.mp4", "-o", "-connections"},
		{"/资料/video.mp4", "--connections=12", "--output=-connections"},
		{"-connections=12", "-output", "-connections", "--", "/资料/video.mp4"},
	} {
		flags, opts := downloadFlags()
		if err := flags.Parse(normalizeDownloadFlags(args)); err != nil {
			t.Fatalf("%q: %v", args, err)
		}
		if opts.connections != 12 || opts.output != "-connections" ||
			!reflect.DeepEqual(flags.Args(), []string{"/资料/video.mp4"}) {
			t.Fatalf("%q: parsed %+v, paths %q", args, opts, flags.Args())
		}
	}
}
