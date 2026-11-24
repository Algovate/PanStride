package baidu

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"
)

var contentChecksumClient = &http.Client{
	Timeout: 15 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" || req.URL.User != nil || !isBaiduDownloadHost(req.URL.Hostname()) || len(via) >= 10 {
			return http.ErrUseLastResponse
		}
		req.Header.Del("Referer")
		return nil
	},
}

func ContentMD5(ctx context.Context, link string, size int64) (string, error) {
	return fetchContentMD5(ctx, contentChecksumClient, link, size)
}

func fetchContentMD5(ctx context.Context, client *http.Client, link string, size int64) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodHead, link, nil)
	if err != nil {
		return "", errors.New("cannot construct download checksum request")
	}
	request.Header.Set("User-Agent", DownloadUserAgent)
	request.Header.Set("Accept-Encoding", "identity")
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errors.New("cannot read download checksum headers")
	}
	defer response.Body.Close()

	coding := response.Header.Get("Content-Encoding")
	if response.StatusCode != http.StatusOK || response.ContentLength != size ||
		(coding != "" && coding != "identity") {
		return "", errors.New("download server did not confirm the full file size")
	}
	value := strings.Trim(strings.TrimSpace(response.Header.Get("Content-MD5")), "\"")
	if checksum := NormalizeMD5(value); checksum != "" {
		return checksum, nil
	}
	if checksum, err := base64.StdEncoding.DecodeString(value); err == nil && len(checksum) == md5.Size {
		return hex.EncodeToString(checksum), nil
	}
	return "", errors.New("download server did not provide a full-file Content-MD5")
}

func NormalizeMD5(raw string) string {
	if decoded, err := hex.DecodeString(raw); err == nil && len(decoded) == md5.Size {
		return strings.ToLower(raw)
	}
	return ""
}
