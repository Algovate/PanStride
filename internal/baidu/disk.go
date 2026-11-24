package baidu

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/baidu-netdisk/baidu-drive-sdk-go/baidudriver/api"
)

const (
	baiduDiskURL      = "https://pan.baidu.com"
	DownloadUserAgent = "pan.baidu.com"
)

type Client struct {
	client  *api.Client
	baseURL string
	auth    authorization
}

func Open() (*Client, error) {
	auth, err := currentAuthorization()
	if err != nil {
		return nil, err
	}
	drive := New(baiduDiskURL, auth.token)
	drive.auth = auth
	return drive, nil
}

func New(baseURL, token string) *Client {
	client := api.NewClient(api.WithBaseURL(baseURL), api.WithAccessToken(token), api.WithHTTPClient(&http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}))
	return &Client{client: client, baseURL: baseURL, auth: authorization{token: token}}
}

func (d *Client) TokenInfo() (source string, expiresAt time.Time) {
	return d.auth.source, d.auth.expiresAt
}

func (d *Client) Account(ctx context.Context) (*api.UInfoResponse, error) {
	user, err := d.client.Nas.UInfo(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("get Baidu account information: %w", apiError(err))
	}
	return user, nil
}

type Entry = api.ListFileInfo

type VisitEntry func(*Entry) (bool, error)

func (d *Client) List(ctx context.Context, dir string, visit VisitEntry) error {
	const pageSize = 1000
	for start := 0; ; start += pageSize {
		page, err := d.client.File.List(ctx, &api.ListParams{
			Dir: dir, Order: api.Ptr("name"), Desc: api.Ptr(0),
			Start: &start, Limit: api.Ptr(pageSize), Showempty: api.Ptr(1),
		})
		if err != nil {
			return fmt.Errorf("list %q: %w", dir, apiError(err))
		}
		for _, entry := range page.List {
			if entry == nil || entry.Path == dir || path.Dir(entry.Path) != dir ||
				path.Clean(entry.Path) != entry.Path {
				return fmt.Errorf("unexpected entry in directory %q", dir)
			}
			stop, err := visit(entry)
			if stop || err != nil {
				return err
			}
		}
		if len(page.List) < pageSize {
			return nil
		}
	}
}

type FileMeta struct {
	api.FileMeta
	ServerMtime int64 `json:"server_mtime"`
}

func (d *Client) FileMetadata(ctx context.Context, id int64) (FileMeta, error) {
	if id <= 0 {
		return FileMeta{}, fmt.Errorf("invalid remote file ID %d", id)
	}
	encoded, err := json.Marshal([]int64{id})
	if err != nil {
		return FileMeta{}, err
	}
	var result struct {
		List []FileMeta `json:"list"`
	}
	err = d.get(ctx, "multimedia", url.Values{
		"method": {"filemetas"}, "dlink": {"1"}, "fsids": {string(encoded)},
	}, &result)
	if err != nil {
		return FileMeta{}, err
	}
	for _, item := range result.List {
		if item.FsID == id && item.Path != "/" && strings.HasPrefix(item.Path, "/") &&
			path.Clean(item.Path) == item.Path && item.Size >= 0 {
			return item, nil
		}
	}
	return FileMeta{}, fmt.Errorf("no usable metadata for file ID %d", id)
}

func (d *Client) get(ctx context.Context, service string, params url.Values, result any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.baseURL+"/rest/2.0/xpan/"+service+"?"+params.Encode(), nil)
	if err != nil {
		return apiError(err)
	}
	req.Header.Set("User-Agent", DownloadUserAgent)
	_, err = d.client.Do(ctx, req, result)
	if err != nil {
		return apiError(err)
	}
	return nil
}

func (d *Client) Search(ctx context.Context, keyword string, visit VisitEntry) error {
	for page := 1; ; page++ {
		var result struct {
			List    []*api.ListFileInfo `json:"list"`
			HasMore int                 `json:"has_more"`
		}
		err := d.get(ctx, "file", url.Values{
			"method": {"search"}, "key": {keyword}, "dir": {"/"},
			"recursion": {"1"}, "page": {strconv.Itoa(page)}, "num": {"100"},
		}, &result)
		if err != nil {
			return fmt.Errorf("search files: %w", err)
		}
		if result.HasMore == 1 && len(result.List) == 0 {
			return fmt.Errorf("Baidu returned an empty search page with more results")
		}
		for _, entry := range result.List {
			if entry == nil || entry.FsID <= 0 || !strings.HasPrefix(entry.Path, "/") ||
				entry.Path == "/" || path.Clean(entry.Path) != entry.Path {
				return fmt.Errorf("Baidu returned an invalid search result")
			}
			stop, err := visit(entry)
			if stop || err != nil {
				return err
			}
		}
		if result.HasMore != 1 {
			return nil
		}
	}
}

func (d *Client) AuthorizeLink(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return "", fmt.Errorf("Baidu returned an invalid HTTPS download link")
	}
	if !isBaiduDownloadHost(u.Hostname()) {
		return "", fmt.Errorf("Baidu returned a download link outside its download domains")
	}
	query := u.Query()
	query.Set("access_token", d.auth.token)
	u.RawQuery = query.Encode()
	u.Fragment = ""
	return u.String(), nil
}

func isBaiduDownloadHost(host string) bool {
	host = strings.ToLower(host)
	for _, domain := range []string{"baidu.com", "baidupcs.com"} {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}
