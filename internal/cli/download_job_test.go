package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"panstride/internal/baidu"
	"panstride/internal/transfer"
)

func TestDownloadKeepGoingReportsFailuresAndContinues(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case q.Get("method") == "list" && q.Get("dir") == "/":
			io.WriteString(w, `{"list":[{"path":"/bad.txt","fs_id":1},{"path":"/blocked","fs_id":2,"isdir":1},{"path":"/done.txt","fs_id":3},{"path":"/saved.txt","fs_id":4}]}`)
		case q.Get("method") == "list" && q.Get("dir") == "/blocked":
			io.WriteString(w, `{"errno":-6}`)
		case q.Get("fsids") == "[1]":
			io.WriteString(w, `{"errno":-9}`)
		case q.Get("fsids") == "[3]":
			io.WriteString(w, `{"list":[{"path":"/done.txt","fs_id":3,"size":4,"dlink":"https://d.pcs.baidu.com/file/item"}]}`)
		case q.Get("fsids") == "[4]":
			io.WriteString(w, `{"list":[{"path":"/saved.txt","fs_id":4,"size":4,"dlink":"https://d.pcs.baidu.com/file/item"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	drive := baidu.New(server.URL, "test-token")
	var out strings.Builder
	job := downloadJob{drive: drive, out: &out, keepGoing: true, transfer: func(_ string, _ string, v transfer.Version) (bool, error) {
		return v.ID == 3, nil
	}}
	err := job.run(context.Background(), downloadSource{path: "/"}, t.TempDir())
	if err == nil {
		t.Fatal("a partial failure must return a nonzero status")
	}
	for _, want := range []string{`"/bad.txt"`, `"/blocked"`, `"/saved.txt"`, "1 saved, 1 skipped, failed files: 1, failed directories: 1"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in output %q", want, out.String())
		}
	}
}

func TestDownloadKeepGoingStopsOnCancellation(t *testing.T) {
	visited := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("method") {
		case "list":
			io.WriteString(w, `{"list":[{"path":"/one","fs_id":1},{"path":"/two","fs_id":2}]}`)
		default:
			io.WriteString(w, `{"list":[{"path":"/one","fs_id":1,"size":4,"dlink":"https://d.pcs.baidu.com/file/item"}]}`)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	drive := baidu.New(server.URL, "test-token")
	job := downloadJob{drive: drive, out: io.Discard, keepGoing: true, transfer: func(string, string, transfer.Version) (bool, error) {
		visited++
		cancel()
		return false, context.Canceled
	}}
	err := job.run(ctx, downloadSource{path: "/"}, t.TempDir())
	if !errors.Is(err, context.Canceled) || visited != 1 || job.fileFailures != 0 {
		t.Fatalf("cancellation was treated as a failure: %v, %d visited, %d failed", err, visited, job.fileFailures)
	}
}

func TestDownloadDirectoryKeepsTree(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("access_token") != "test-token" {
			http.Error(w, "missing authorization", 401)
			return
		}
		switch q.Get("method") {
		case "list":
			switch q.Get("dir") {
			case "/":
				io.WriteString(w, `{"list":[{"path":"/课程","isdir":1}]}`)
			case "/课程":
				io.WriteString(w, `{"list":[{"path":"/课程/示例.txt","fs_id":42,"size":4},{"path":"/课程/空","isdir":1}]}`)
			case "/课程/空":
				io.WriteString(w, `{"list":[]}`)
			default:
				http.NotFound(w, r)
			}
		case "filemetas":
			if q.Get("fsids") != "[42]" || q.Get("dlink") != "1" {
				http.Error(w, "missing file ID or dlink", 400)
				return
			}
			io.WriteString(w, `{"list":[{"fs_id":42,"path":"/课程/示例.txt","size":4,"dlink":"https://d.pcs.baidu.com/file/item?sign=abc"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	output := t.TempDir()
	drive := baidu.New(server.URL, "test-token")
	job := downloadJob{drive: drive, out: io.Discard, transfer: func(link, target string, version transfer.Version) (bool, error) {
		u, err := url.Parse(link)
		if err != nil {
			return false, err
		}
		if u.Query().Get("access_token") != "test-token" || u.Query().Get("sign") != "abc" || version.Size != 4 {
			t.Fatalf("unexpected transfer: %q, %d", link, version.Size)
		}
		return false, os.WriteFile(target, []byte("data"), 0600)
	}}
	err := job.run(context.Background(), downloadSource{path: "/课程"}, output)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(output, "课程", "示例.txt"))
	if err != nil || string(data) != "data" {
		t.Fatalf("unexpected downloaded file %q, %v", data, err)
	}
	info, err := os.Stat(filepath.Join(output, "课程", "空"))
	if err != nil || !info.IsDir() {
		t.Fatalf("empty folder was not preserved: %v", err)
	}
}

func TestDownloadJobRefreshesStalledLink(t *testing.T) {
	for _, test := range []struct {
		name       string
		newVersion int
		stallAgain bool
		wantTries  int
		wantError  string
	}{
		{name: "same version", newVersion: 1, wantTries: 2},
		{name: "changed version", newVersion: 2, wantTries: 1, wantError: "remote file changed"},
		{name: "retry limit", newVersion: 1, stallAgain: true, wantTries: 2, wantError: "stalled again"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				request := requests.Add(1)
				version := 1
				if request > 1 {
					version = test.newVersion
				}
				fmt.Fprintf(w, `{"list":[{"fs_id":42,"path":"/file","size":4,"server_mtime":%d,"dlink":"https://d.pcs.baidu.com/file?ticket=%d"}]}`, version, request)
			}))
			defer server.Close()

			var out strings.Builder
			var tries int
			job := downloadJob{drive: baidu.New(server.URL, "test-token"), out: &out}
			job.transfer = func(link, target string, version transfer.Version) (bool, error) {
				tries++
				u, err := url.Parse(link)
				if err != nil || u.Query().Get("ticket") != fmt.Sprint(tries) || version.Modified != 1 {
					t.Fatal("transfer attempt did not use the expected link and remote version")
				}
				if tries == 1 || test.stallAgain {
					return false, transfer.ErrStalled
				}
				return false, nil
			}
			err := job.run(context.Background(), downloadSource{id: 42}, t.TempDir())
			if test.wantError == "" && err != nil || test.wantError != "" && (err == nil || !strings.Contains(err.Error(), test.wantError)) {
				t.Fatalf("retry returned %v", err)
			}
			if tries != test.wantTries || requests.Load() != 2 || strings.Contains(out.String(), "test-token") {
				t.Fatalf("unexpected retry: %d transfers, %d metadata requests, %q", tries, requests.Load(), out.String())
			}
		})
	}
}

func TestAutomaticStallMonitorIsForLargeFiles(t *testing.T) {
	options := downloadOptions{stallTimeout: defaultStallTimeout}
	if options.stallTimeoutFor(automaticStallMinSize-1) != 0 ||
		options.stallTimeoutFor(automaticStallMinSize) != 45*time.Second {
		t.Fatal("automatic stall monitor did not respect the large-file threshold")
	}
	options.stallTimeoutSet = true
	if options.stallTimeoutFor(1) != 45*time.Second {
		t.Fatal("explicit timeout did not enable the monitor for a small file")
	}
	options.stallTimeout = 0
	if options.stallTimeoutFor(automaticStallMinSize) != 0 {
		t.Fatal("explicit zero did not disable the default monitor")
	}
}
