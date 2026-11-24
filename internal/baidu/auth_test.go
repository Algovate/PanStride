package baidu

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDownloadLinkRejectsUntrustedHost(t *testing.T) {
	if _, err := New("https://pan.baidu.com", "test-token").AuthorizeLink("https://baidu.com.example.org/file"); err == nil {
		t.Fatal("added an access token to an untrusted host")
	}
}

func TestBrowserLoginUsesLinkSwiftOAuth(t *testing.T) {
	var out strings.Builder
	cred, err := login(&out, func() (string, error) {
		lines := strings.Split(out.String(), "\n")
		var browserURL string
		for _, line := range lines {
			if strings.HasPrefix(line, "https://") {
				browserURL = line
				break
			}
		}
		u, err := url.Parse(browserURL)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		if u.Host != "openapi.baidu.com" || u.Path != "/oauth/2.0/authorize" ||
			q.Get("response_type") != "token" || q.Get("redirect_uri") != "oob" ||
			q.Get("scope") != "basic,netdisk" || q.Get("confirm_login") != "0" ||
			q.Get("client_id") != "omiOnr2tYnN9vSyDErcVFWpPU2mZA7YO" || q.Get("state") == "" {
			t.Fatalf("unexpected browser authorization URL %q", u)
		}
		return "https://openapi.baidu.com/oauth/2.0/login_success#access_token=test%2Btoken&expires_in=3600&scope=basic+netdisk&state=" + q.Get("state"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if cred.AccessToken != "test+token" || !cred.ExpiresAt.After(time.Now()) {
		t.Fatal("browser callback did not return a valid token")
	}
	if strings.Contains(out.String(), "test+token") {
		t.Fatal("authorization output revealed the access token")
	}
	target := filepath.Join(t.TempDir(), "credentials.json")
	if err := saveCredentials(target, cred); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0077 != 0 {
		t.Fatalf("credential file permissions: %v", info.Mode().Perm())
	}
}

func TestBrowserCallbackRejectsInvalidAuthorization(t *testing.T) {
	for name, raw := range map[string]string{
		"wrong host":    "https://example.com/oauth/2.0/login_success#access_token=secret&expires_in=3600&scope=netdisk&state=expected",
		"wrong state":   "https://openapi.baidu.com/oauth/2.0/login_success#access_token=secret&expires_in=3600&scope=netdisk&state=other",
		"no scope":      "https://openapi.baidu.com/oauth/2.0/login_success#access_token=secret&expires_in=3600&scope=basic&state=expected",
		"expired token": "https://openapi.baidu.com/oauth/2.0/login_success#access_token=secret&expires_in=0&scope=netdisk&state=expected",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := browserCredentials(raw, "expected")
			if err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatal("expected a safe callback error")
			}
		})
	}
}
