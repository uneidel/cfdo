package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// adminClient talks to the __cfdo/ routes of the deployed worker. This is the
// only way to reach Durable Object storage: the Cloudflare API cannot read it.
type adminClient struct {
	base   string
	secret string
	http   *http.Client
}

func newAdminClient(base, secret string) *adminClient {
	return &adminClient{
		base:   strings.TrimRight(base, "/"),
		secret: secret,
		http:   &http.Client{Timeout: 2 * time.Minute},
	}
}

func (a *adminClient) do(ctx context.Context, method, op string, q url.Values, body []byte) ([]byte, error) {
	u := a.base + "/__cfdo/" + op
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var last error
	for attempt := 1; attempt <= 3; attempt++ {
		var rdr io.Reader
		if body != nil {
			rdr = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, u, rdr)
		if err != nil {
			return nil, err
		}
		req.Header.Set("x-cfdo-secret", a.secret)
		req.Header.Set("Accept", "application/json")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := a.http.Do(req)
		if err != nil {
			last = err
			if !sleepBackoff(ctx, attempt, 0) {
				return nil, err
			}
			continue
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 512<<20))
		resp.Body.Close()
		if readErr != nil {
			last = readErr
			if !sleepBackoff(ctx, attempt, 0) {
				return nil, readErr
			}
			continue
		}
		switch {
		case resp.StatusCode == http.StatusUnauthorized:
			return nil, fmt.Errorf("worker rejected CFDO_SECRET (401) — the local secret does not match the one bound at upload time")
		case resp.StatusCode == http.StatusNotFound:
			return nil, fmt.Errorf("worker has no /__cfdo/%s route (404) — is it running the cfdo-generated module?", op)
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			last = fmt.Errorf("worker returned %d: %s", resp.StatusCode, strings.TrimSpace(shorten(string(raw), 200)))
			if !sleepBackoff(ctx, attempt, 0) {
				return nil, last
			}
			continue
		case resp.StatusCode >= 400:
			return nil, fmt.Errorf("worker returned %d: %s", resp.StatusCode, strings.TrimSpace(shorten(string(raw), 200)))
		}
		return raw, nil
	}
	return nil, last
}

type pingResult struct {
	OK      bool   `json:"ok"`
	Class   string `json:"class"`
	Binding string `json:"binding"`
	Format  int    `json:"format"`
}

func (a *adminClient) Ping(ctx context.Context) (*pingResult, error) {
	raw, err := a.do(ctx, http.MethodGet, "ping", nil, nil)
	if err != nil {
		return nil, err
	}
	var p pingResult
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("unexpected ping response: %s", shorten(string(raw), 120))
	}
	return &p, nil
}

type indexEntry struct {
	Name string `json:"name"`
	ID   string `json:"id"`
}

// List asks the worker's index object which objects it has routed to. This is
// the only discovery path that works for SQLite-backed namespaces, whose
// objects the Cloudflare listing API does not return.
func (a *adminClient) List(ctx context.Context) ([]indexEntry, error) {
	raw, err := a.do(ctx, http.MethodGet, "list", nil, nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Objects []indexEntry `json:"objects"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("unexpected list response: %s", shorten(string(raw), 120))
	}
	return out.Objects, nil
}

// Export returns the raw JSON body for one object, kept as bytes so the
// backup file is exactly what the object produced.
func (a *adminClient) Export(ctx context.Context, id string) ([]byte, error) {
	return a.do(ctx, http.MethodGet, "export", url.Values{"id": {id}}, nil)
}

type importResult struct {
	OK    bool   `json:"ok"`
	Mode  string `json:"mode"`
	KV    int    `json:"kv"`
	Rows  int    `json:"rows"`
	Error string `json:"error"`
}

func (a *adminClient) Import(ctx context.Context, id, mode string, body []byte) (*importResult, error) {
	raw, err := a.do(ctx, http.MethodPost, "import", url.Values{"id": {id}, "mode": {mode}}, body)
	if err != nil {
		return nil, err
	}
	var r importResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("unexpected import response: %s", shorten(string(raw), 120))
	}
	if !r.OK {
		return nil, fmt.Errorf("object refused the import: %s", r.Error)
	}
	return &r, nil
}
