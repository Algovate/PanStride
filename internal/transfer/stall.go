package transfer

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

const ariaDownloadID = "0000000000000001"

var ErrStalled = errors.New("download stalled")

type ariaControl struct {
	endpoint string
	secret   string
	config   string
	client   *http.Client
}

func newAriaControl(stateDir string) (*ariaControl, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	addr := listener.Addr().(*net.TCPAddr)
	if err := listener.Close(); err != nil {
		return nil, err
	}

	conf, err := os.CreateTemp(stateDir, "aria2-*.conf")
	if err != nil {
		return nil, err
	}
	secret := rand.Text()
	_, writeErr := fmt.Fprintf(conf, "enable-rpc=true\nrpc-listen-all=false\nrpc-listen-port=%d\nrpc-secret=%s\nstop-with-process=%d\n", addr.Port, secret, os.Getpid())
	if err := errors.Join(writeErr, conf.Close()); err != nil {
		os.Remove(conf.Name())
		return nil, err
	}

	return &ariaControl{
		endpoint: "http://" + addr.String() + "/jsonrpc",
		secret:   secret,
		config:   conf.Name(),
		client:   &http.Client{Transport: &http.Transport{}, Timeout: 750 * time.Millisecond},
	}, nil
}

func (r *ariaControl) close() {
	r.client.CloseIdleConnections()
	os.Remove(r.config)
}

func (r *ariaControl) call(ctx context.Context, method string, args []any, out any) error {
	payload, err := json.Marshal(struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Method  string `json:"method"`
		Params  []any  `json:"params"`
	}{"2.0", 1, method, append([]any{"token:" + r.secret}, args...)})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	var reply struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 16<<10)).Decode(&reply); err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK || len(reply.Result) == 0 {
		return errors.New("aria2 control request failed")
	}
	if out != nil {
		return json.Unmarshal(reply.Result, out)
	}
	return nil
}

func (r *ariaControl) shutdown(ctx context.Context) error {
	return r.call(ctx, "aria2.shutdown", nil, nil)
}

type ariaStatus struct {
	State     string `json:"status"`
	Completed int64  `json:"completedLength,string"`
	ErrorCode int    `json:"errorCode,string"`
}

func (r *ariaControl) watch(ctx context.Context, timeout time.Duration, cancel context.CancelCauseFunc) {
	interval := min(time.Second, max(50*time.Millisecond, timeout/4))
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	lastProgress := time.Now()
	lastContact := lastProgress
	var completed int64
	for {
		var status ariaStatus
		err := r.call(ctx, "aria2.tellStatus", []any{
			ariaDownloadID, []string{"status", "completedLength", "errorCode"},
		}, &status)
		now := time.Now()
		if err == nil {
			lastContact = now
			switch status.State {
			case "complete":
				if r.shutdown(ctx) == nil {
					return
				}
			case "error", "removed":
				cancel(fmt.Errorf("aria2 transfer ended with status %s (code %d)", status.State, status.ErrorCode))
				return
			}
			if status.Completed > completed {
				completed = status.Completed
				lastProgress = now
			}
		}
		if ctx.Err() != nil {
			return
		}
		if now.Sub(lastContact) >= timeout {
			cancel(errors.New("aria2 control channel stopped responding"))
			return
		}
		if err == nil && now.Sub(lastProgress) >= timeout {
			cancel(fmt.Errorf("%w: no new data for %s", ErrStalled, timeout))
			return
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
