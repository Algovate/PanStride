package transfer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestStalledTransferResumesFromRecordedBytes(t *testing.T) {
	aria, err := exec.LookPath("aria2c")
	if err != nil {
		t.Skip("aria2c not installed")
	}
	payload := strings.Repeat("0123456789abcdef", 350000)
	var retry, resumed atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if retry.Load() {
			if header := r.Header.Get("Range"); header != "" && !strings.HasPrefix(header, "bytes=0-") {
				resumed.Store(true)
			}
			http.ServeContent(w, r, "file", time.Time{}, strings.NewReader(payload))
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		w.Header().Set("Accept-Ranges", "bytes")
		io.WriteString(w, payload[:2<<20])
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	target := filepath.Join(t.TempDir(), "file")
	version := Version{ID: 42, Size: int64(len(payload)), Modified: 1}
	if _, err := Download(ctx, aria, server.URL, target, version, Options{Connections: 1, UserAgent: "pan.baidu.com", StallTimeout: 2 * time.Second}, io.Discard); !errors.Is(err, ErrStalled) {
		t.Fatalf("wanted a stall error, got %v", err)
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stalled transfer published a partial file: %v", err)
	}

	retry.Store(true)
	if _, err := Download(ctx, aria, server.URL, target, version, Options{Connections: 1, UserAgent: "pan.baidu.com", StallTimeout: 2 * time.Second}, io.Discard); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target)
	if err != nil || !resumed.Load() || string(data) != payload {
		t.Fatalf("stalled download did not resume with intact bytes: %v", err)
	}
}

func TestSlowTransferWithNewBytesDoesNotStall(t *testing.T) {
	aria, err := exec.LookPath("aria2c")
	if err != nil {
		t.Skip("aria2c not installed")
	}
	payload := strings.Repeat("0123456789abcdef", 16*1024)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		for offset := 0; offset < len(payload); offset += 64 << 10 {
			io.WriteString(w, payload[offset:offset+(64<<10)])
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(700 * time.Millisecond):
			}
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	target := filepath.Join(t.TempDir(), "file")
	version := Version{ID: 42, Size: int64(len(payload)), Modified: 1}
	if _, err := Download(ctx, aria, server.URL, target, version, Options{Connections: 1, UserAgent: "pan.baidu.com", StallTimeout: 1500 * time.Millisecond}, io.Discard); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != payload {
		t.Fatalf("slow download did not finish intact: %v", err)
	}
}
