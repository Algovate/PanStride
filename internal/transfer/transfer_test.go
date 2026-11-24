package transfer

import (
	"context"
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

type progressOutput struct {
	strings.Builder
	seen chan struct{}
}

func (p *progressOutput) Write(data []byte) (int, error) {
	if strings.HasPrefix(string(data), "Progress:") {
		select {
		case p.seen <- struct{}{}:
		default:
		}
	}
	return p.Builder.Write(data)
}

func TestAriaWritesFileAndProtectsExistingFile(t *testing.T) {
	binary, err := exec.LookPath("aria2c")
	if err != nil {
		t.Skip("aria2c not installed")
	}
	payload := strings.Repeat("0123456789abcdef", 350000)
	var ranges atomic.Int32
	var reportedBeforeEOF atomic.Bool
	progress := progressOutput{seen: make(chan struct{}, 1)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.UserAgent() != "pan.baidu.com" {
			http.Error(w, "missing User-Agent", 403)
			return
		}
		if r.Header.Get("Range") != "" {
			ranges.Add(1)
			http.ServeContent(w, r, "data.txt", time.Time{}, strings.NewReader(payload))
			return
		}

		// Give aria2 time to schedule range requests while the first connection is active.
		w.Header().Set("Content-Length", "5600000")
		w.Header().Set("Accept-Ranges", "bytes")
		io.WriteString(w, payload[:4096])
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-progress.seen:
			reportedBeforeEOF.Store(true)
			io.WriteString(w, payload[4096:])
		case <-time.After(3 * time.Second):
			io.WriteString(w, payload[4096:])
		}
	}))
	defer server.Close()

	target := filepath.Join(t.TempDir(), "data.txt")
	if _, err := Download(context.Background(), binary, server.URL, target, Version{ID: 12, Size: int64(len(payload)), Modified: 1}, Options{Connections: 4, UserAgent: "pan.baidu.com"}, &progress); err != nil {
		t.Fatal(err)
	}
	if !reportedBeforeEOF.Load() || !strings.Contains(progress.String(), "/s") {
		t.Fatalf("expected live transfer progress, got %q", progress.String())
	}
	if ranges.Load() == 0 {
		t.Fatal("aria2 did not request any ranges")
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != payload {
		t.Fatalf("downloaded file differs, error: %v", err)
	}
	if _, err := Download(context.Background(), binary, server.URL, target, Version{ID: 12, Size: 1, Modified: 1}, Options{Connections: 4, UserAgent: "pan.baidu.com"}, io.Discard); err == nil {
		t.Fatal("expected a conflict for an existing file with a different size")
	}
}

func TestAriaResumesInterruptedFile(t *testing.T) {
	binary, err := exec.LookPath("aria2c")
	if err != nil {
		t.Skip("aria2c not installed")
	}
	payload := strings.Repeat("0123456789abcdef", 350000)
	var retry atomic.Bool
	var resumed atomic.Bool
	partial := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if retry.Load() {
			if r.Header.Get("Range") != "" && !strings.HasPrefix(r.Header.Get("Range"), "bytes=0-") {
				resumed.Store(true)
			}
			http.ServeContent(w, r, "data.txt", time.Time{}, strings.NewReader(payload))
			return
		}
		w.Header().Set("Content-Length", "5600000")
		w.Header().Set("Accept-Ranges", "bytes")
		io.WriteString(w, payload[:2*1024*1024])
		w.(http.Flusher).Flush()
		select {
		case partial <- struct{}{}:
		default:
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	target := filepath.Join(t.TempDir(), "data.txt")
	stop := make(chan error, 1)
	go func() {
		_, err := Download(ctx, binary, server.URL, target, Version{ID: 12, Size: int64(len(payload)), Modified: 1}, Options{Connections: 1, UserAgent: "pan.baidu.com"}, io.Discard)
		stop <- err
	}()
	select {
	case <-partial:
	case err := <-stop:
		t.Fatalf("transfer stopped before receiving partial content: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	time.Sleep(250 * time.Millisecond)
	cancel()
	if err := <-stop; err != context.Canceled {
		t.Fatalf("expected cancellation, got %v", err)
	}

	retry.Store(true)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel2()
	if _, err := Download(ctx2, binary, server.URL, target, Version{ID: 12, Size: int64(len(payload)), Modified: 1}, Options{Connections: 1, UserAgent: "pan.baidu.com"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target)
	if !resumed.Load() || err != nil || string(data) != payload {
		t.Fatalf("transfer did not resume with intact contents: %v", err)
	}
}
