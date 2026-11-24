package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"panstride/internal/baidu"
	"panstride/internal/transfer"
)

func TestDownloadByIDUsesMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		q := req.URL.Query()
		if q.Get("method") != "filemetas" || q.Get("fsids") != "[42]" || q.Get("access_token") != "test-token" {
			http.Error(w, "unexpected metadata request", 400)
			return
		}
		io.WriteString(w, `{"list":[{"fs_id":42,"path":"/资料/示例.txt","size":4,"server_mtime":123,"dlink":"https://d.pcs.baidu.com/file/item"}]}`)
	}))
	defer server.Close()
	drive := baidu.New(server.URL, "test-token")
	output := t.TempDir()
	called := false
	job := downloadJob{drive: drive, out: io.Discard, transfer: func(link, target string, version transfer.Version) (bool, error) {
		called = true
		if target != filepath.Join(output, "示例.txt") || version.ID != 42 || version.Modified != 123 || version.Size != 4 {
			t.Fatalf("unexpected metadata %q, %+v", target, version)
		}
		return false, nil
	}}
	err := job.run(context.Background(), downloadSource{id: 42}, output)
	if err != nil || !called {
		t.Fatalf("download by ID failed: %v", err)
	}
}

func TestSearchFollowsPagesAndPrintsIDs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		q := req.URL.Query()
		if q.Get("method") != "search" || q.Get("key") != "示例" || q.Get("dir") != "/" || q.Get("recursion") != "1" {
			http.Error(w, "unexpected search request", 400)
			return
		}
		switch q.Get("page") {
		case "1":
			io.WriteString(w, `{"has_more":1,"list":[{"fs_id":42,"path":"/示例一.txt","size":4}]}`)
		case "2":
			io.WriteString(w, `{"has_more":0,"list":[{"fs_id":84,"path":"/资料/示例二.txt","size":8}]}`)
		default:
			http.NotFound(w, req)
		}
	}))
	defer server.Close()
	drive := baidu.New(server.URL, "test-token")
	var out strings.Builder
	err := writeListing(&out, listingFormat{showIDs: true}, func(visit baidu.VisitEntry) error {
		return drive.Search(context.Background(), "示例", visit)
	})
	if err != nil || out.String() != "42\t4\t/示例一.txt\n84\t8\t/资料/示例二.txt\n" {
		t.Fatalf("unexpected search output %q, %v", out.String(), err)
	}
}
