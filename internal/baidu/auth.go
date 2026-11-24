package baidu

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/baidu-netdisk/baidu-drive-sdk-go/baidudriver/api"
	"golang.org/x/term"
)

type credentials struct {
	AccessToken string    `json:"access_token"`
	ExpiresAt   time.Time `json:"expires_at"`
}

const (
	baiduOAuthHost = "openapi.baidu.com"
	// LinkSwift identifies its default browser OAuth application as OpenList.
	baiduClientID = "omiOnr2tYnN9vSyDErcVFWpPU2mZA7YO"
)

func Login(out io.Writer) error {
	target, err := credentialPath()
	if err != nil {
		return err
	}
	cred, err := login(out, readCallback)
	if err != nil {
		return err
	}
	return saveCredentials(target, cred)
}

func login(out io.Writer, readCallback func() (string, error)) (credentials, error) {
	state := rand.Text()
	authorize := url.URL{
		Scheme: "https",
		Host:   baiduOAuthHost,
		Path:   "/oauth/2.0/authorize",
		RawQuery: url.Values{
			"client_id":     {baiduClientID},
			"response_type": {"token"},
			"scope":         {"basic,netdisk"},
			"redirect_uri":  {"oob"},
			"confirm_login": {"0"},
			"state":         {state},
		}.Encode(),
	}
	fmt.Fprintln(out, "Open this address in your browser and authorize the OpenList application:")
	fmt.Fprintln(out, authorize.String())
	fmt.Fprint(out, "\nPaste the full address of the Baidu authorization success page: ")

	raw, err := readCallback()
	fmt.Fprintln(out)
	if err != nil {
		return credentials{}, fmt.Errorf("read authorization address: %w", err)
	}
	return browserCredentials(raw, state)
}

func browserCredentials(raw, state string) (credentials, error) {
	callback, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || callback.Scheme != "https" || callback.Host != baiduOAuthHost ||
		callback.Path != "/oauth/2.0/login_success" || callback.User != nil {
		return credentials{}, errors.New("expected the Baidu authorization success page address")
	}
	params, err := url.ParseQuery(callback.EscapedFragment())
	if err != nil || state == "" || params.Get("state") != state {
		return credentials{}, errors.New("authorization does not match this login; use the new browser address from panstride login")
	}
	if params.Get("error") != "" {
		return credentials{}, errors.New("Baidu did not grant authorization")
	}
	scopes := strings.Fields(strings.ReplaceAll(params.Get("scope"), ",", " "))
	if !slices.Contains(scopes, "netdisk") {
		return credentials{}, errors.New("Baidu did not grant netdisk access")
	}
	token := params.Get("access_token")
	seconds, err := strconv.ParseInt(params.Get("expires_in"), 10, 32)
	if token == "" || err != nil || seconds <= 0 {
		return credentials{}, errors.New("authorization address is missing a valid access token or expiration")
	}
	return credentials{AccessToken: token, ExpiresAt: time.Now().Add(time.Duration(seconds) * time.Second)}, nil
}

func readCallback() (string, error) {
	input := bufio.NewReader(os.Stdin)
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		line, err := input.ReadString('\n')
		if errors.Is(err, io.EOF) && line != "" {
			err = nil
		}
		return line, err
	}

	// Raw mode keeps the callback hidden and lets Ctrl+C restore the terminal before exiting.
	state, err := term.MakeRaw(fd)
	if err != nil {
		return "", err
	}
	defer term.Restore(fd, state)

	var line []byte
	for {
		b, err := input.ReadByte()
		if err != nil {
			return "", err
		}
		switch b {
		case '\r', '\n':
			return string(line), nil
		case 3, 4: // Ctrl+C or Ctrl+D
			return "", context.Canceled
		case 8, 127: // Backspace or DEL
			if len(line) > 0 {
				line = line[:len(line)-1]
			}
		default:
			line = append(line, b)
		}
	}
}

func credentialPath() (string, error) {
	config, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(config, "panstride", "credentials.json"), nil
}

func saveCredentials(target string, cred credentials) error {
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".credentials-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	writeErr := json.NewEncoder(f).Encode(cred)
	if err := errors.Join(writeErr, f.Close()); err != nil {
		return err
	}
	return os.Rename(f.Name(), target)
}

type authorization struct {
	token     string
	source    string
	expiresAt time.Time
}

func currentAuthorization() (authorization, error) {
	if token := strings.TrimSpace(os.Getenv("BAIDU_ACCESS_TOKEN")); token != "" {
		return authorization{token: token, source: "BAIDU_ACCESS_TOKEN"}, nil
	}
	target, err := credentialPath()
	if err != nil {
		return authorization{}, err
	}
	data, err := os.ReadFile(target)
	if os.IsNotExist(err) {
		return authorization{}, errors.New("run panstride login or set BAIDU_ACCESS_TOKEN")
	}
	if err != nil {
		return authorization{}, err
	}
	var cred credentials
	if err := json.Unmarshal(data, &cred); err != nil {
		return authorization{}, errors.New("cannot read saved authorization; run panstride login")
	}
	if cred.AccessToken == "" || !time.Now().Before(cred.ExpiresAt) {
		return authorization{}, errors.New("authorization has expired; run panstride login")
	}
	return authorization{token: cred.AccessToken, source: "saved login", expiresAt: cred.ExpiresAt}, nil
}

func apiError(err error) error {
	var failure *api.APIError
	if errors.As(err, &failure) {
		if failure.Errno == 20016 || failure.Errno == 20017 || failure.Errno == 9019 {
			return fmt.Errorf("Baidu rejected the access token (code %d); run panstride login or update BAIDU_ACCESS_TOKEN", failure.Errno)
		}
		if failure.Response != nil {
			return fmt.Errorf("Baidu API returned code %d (HTTP %d)", failure.Errno, failure.Response.StatusCode)
		}
		return fmt.Errorf("Baidu API returned code %d", failure.Errno)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	// Transport errors may contain access tokens in their URLs.
	return errors.New("could not complete the request to Baidu")
}
