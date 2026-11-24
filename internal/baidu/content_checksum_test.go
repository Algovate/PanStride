package baidu

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchContentMD5VerifiesFullLength(t *testing.T) {
	checksum := md5.Sum([]byte("data"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodHead || req.Header.Get("Accept-Encoding") != "identity" {
			http.Error(w, "expected an identity HEAD request", 400)
			return
		}
		w.Header().Set("Content-Length", "4")
		w.Header().Set("Content-MD5", base64.StdEncoding.EncodeToString(checksum[:]))
	}))
	defer server.Close()
	got, err := fetchContentMD5(context.Background(), server.Client(), server.URL, 4)
	if err != nil || got != fmt.Sprintf("%x", checksum) {
		t.Fatalf("unexpected checksum or error: %q, %v", got, err)
	}
	if _, err := fetchContentMD5(context.Background(), server.Client(), server.URL, 5); err == nil {
		t.Fatal("used an HTTP digest for the wrong file size")
	}
}

func TestContentChecksumRedirectKeepsCredentialsWithinBaidu(t *testing.T) {
	for _, target := range []string{"http://d.pcs.baidu.com/file", "https://baidu.com.example.test/file"} {
		req, err := http.NewRequest(http.MethodHead, target, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := contentChecksumClient.CheckRedirect(req, nil); !errors.Is(err, http.ErrUseLastResponse) {
			t.Fatalf("accepted an unsafe checksum redirect: %s", target)
		}
	}
	req, err := http.NewRequest(http.MethodHead, "https://cdn.baidupcs.com/file", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Referer", "https://d.pcs.baidu.com/file?access_token=diagnostic")
	if err := contentChecksumClient.CheckRedirect(req, nil); err != nil || req.Header.Get("Referer") != "" {
		t.Fatal("trusted checksum redirect did not strip the authenticated referer")
	}
}
