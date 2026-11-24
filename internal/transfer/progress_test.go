package transfer

import (
	"io"
	"strings"
	"testing"
	"time"
)

func TestProgressHidesAriaLogs(t *testing.T) {
	var out strings.Builder
	reporter := transferProgress{out: &out}
	io.WriteString(&reporter, "Download failed: https://example.test/file?access_token=sensitive\n")
	io.WriteString(&reporter, "[#3faedd 3.0MiB/35MiB(8%) CN:8 DL:")
	io.WriteString(&reporter, "1.1MiB ETA:29s]\r")
	expected := "Progress: 8% | 3.0 MiB / 35 MiB | 1.1 MiB/s | 8 connections | ETA 29s\n"
	if out.String() != expected {
		t.Fatalf("unexpected progress output %q", out.String())
	}
}

func TestProgressSmoothsShortDropAndResetsOnStall(t *testing.T) {
	start := time.Unix(1, 0)
	var rate transferRate
	if got := rate.update(start, 1024*1024); got != 1024*1024 {
		t.Fatalf("first speed %f", got)
	}
	got := rate.update(start.Add(time.Second), 128*1024)
	if got <= 512*1024 || got >= 1024*1024 {
		t.Fatalf("speed did not smooth temporary drop: %f", got)
	}
	rate.update(start.Add(2*time.Second), 0)
	if got := rate.update(start.Add(7*time.Second), 0); got != 0 {
		t.Fatalf("speed held a stale estimate through a stall: %f", got)
	}
}
