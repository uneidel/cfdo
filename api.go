package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const defaultAPIBase = "https://api.cloudflare.com/client/v4"

// Client is a minimal Cloudflare API v4 client covering the Workers and
// Durable Objects endpoints cfdo needs.
type Client struct {
	Token string
	Base  string
	HTTP  *http.Client
}

func NewClient(token string) *Client {
	base := defaultAPIBase
	// Escape hatch for tests and for accounts fronted by an API gateway.
	if v := os.Getenv("CFDO_API_BASE"); v != "" {
		base = strings.TrimRight(v, "/")
	}
	return &Client{
		Token: token,
		Base:  base,
		HTTP:  &http.Client{Timeout: 3 * time.Minute},
	}
}

type APIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type resultInfo struct {
	Cursor     string `json:"cursor"`
	Page       int    `json:"page"`
	TotalCount int    `json:"total_count"`
}

type envelope struct {
	Success    bool            `json:"success"`
	Errors     []APIError      `json:"errors"`
	Messages   []APIError      `json:"messages"`
	Result     json.RawMessage `json:"result"`
	ResultInfo *resultInfo     `json:"result_info"`
}

// HTTPError carries the Cloudflare error list so callers can match on codes.
type HTTPError struct {
	Status int
	Errors []APIError
	Body   string
}

func (e *HTTPError) Error() string {
	if len(e.Errors) > 0 {
		parts := make([]string, 0, len(e.Errors))
		for _, err := range e.Errors {
			parts = append(parts, fmt.Sprintf("%s (code %d)", err.Message, err.Code))
		}
		return fmt.Sprintf("cloudflare API %d: %s", e.Status, strings.Join(parts, "; "))
	}
	body := e.Body
	if len(body) > 400 {
		body = body[:400] + "…"
	}
	return fmt.Sprintf("cloudflare API %d: %s", e.Status, body)
}

func (e *HTTPError) has(code int) bool {
	for _, err := range e.Errors {
		if err.Code == code {
			return true
		}
	}
	return false
}

// request performs an API call, retrying idempotently on rate limits and
// transient server errors, and unmarshals the envelope's result into out.
func (c *Client) request(ctx context.Context, method, path string, body []byte, contentType string, out any) (*resultInfo, error) {
	const maxAttempts = 4
	var lastErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		var rdr io.Reader
		if body != nil {
			rdr = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.Base+path, rdr)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.Token)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "cfdo/1.0")
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}

		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = err
			if !sleepBackoff(ctx, attempt, 0) {
				return nil, err
			}
			continue
		}

		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			if !sleepBackoff(ctx, attempt, 0) {
				return nil, readErr
			}
			continue
		}

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = &HTTPError{Status: resp.StatusCode, Body: string(raw)}
			if !sleepBackoff(ctx, attempt, retryAfter(resp)) {
				return nil, lastErr
			}
			continue
		}

		var env envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, &HTTPError{Status: resp.StatusCode, Body: string(raw)}
		}
		if resp.StatusCode >= 400 || !env.Success {
			return nil, &HTTPError{Status: resp.StatusCode, Errors: env.Errors, Body: string(raw)}
		}
		if out != nil && len(env.Result) > 0 && string(env.Result) != "null" {
			if err := json.Unmarshal(env.Result, out); err != nil {
				return nil, fmt.Errorf("decoding result of %s %s: %w", method, path, err)
			}
		}
		return env.ResultInfo, nil
	}
	return nil, lastErr
}

func retryAfter(resp *http.Response) time.Duration {
	if v := resp.Header.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return 0
}

func sleepBackoff(ctx context.Context, attempt int, hint time.Duration) bool {
	d := hint
	if d <= 0 {
		d = time.Duration(1<<uint(attempt-1)) * 500 * time.Millisecond
	}
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// ---------------------------------------------------------------- Durable Objects

type Namespace struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Script    string `json:"script"`
	Class     string `json:"class"`
	UseSQLite bool   `json:"use_sqlite"`
}

type DOObject struct {
	ID            string `json:"id"`
	HasStoredData bool   `json:"hasStoredData"`
}

func (c *Client) ListNamespaces(ctx context.Context, accountID string) ([]Namespace, error) {
	var out []Namespace
	_, err := c.request(ctx, http.MethodGet,
		fmt.Sprintf("/accounts/%s/workers/durable_objects/namespaces", url.PathEscape(accountID)),
		nil, "", &out)
	return out, err
}

// FindNamespace locates the namespace backing a script's class.
func (c *Client) FindNamespace(ctx context.Context, accountID, script, class string) (*Namespace, error) {
	nss, err := c.ListNamespaces(ctx, accountID)
	if err != nil {
		return nil, err
	}
	for i := range nss {
		if nss[i].Script == script && nss[i].Class == class {
			return &nss[i], nil
		}
	}
	return nil, fmt.Errorf("no Durable Object namespace for class %q of script %q (has it been uploaded with a migration?)", class, script)
}

// ListObjects walks every page of a namespace. Cloudflare only returns
// objects that currently hold stored data.
func (c *Client) ListObjects(ctx context.Context, accountID, namespaceID string) ([]DOObject, error) {
	var all []DOObject
	cursor := ""
	for {
		q := url.Values{"limit": {"1000"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var page []DOObject
		info, err := c.request(ctx, http.MethodGet,
			fmt.Sprintf("/accounts/%s/workers/durable_objects/namespaces/%s/objects?%s",
				url.PathEscape(accountID), url.PathEscape(namespaceID), q.Encode()),
			nil, "", &page)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if info == nil || info.Cursor == "" || len(page) == 0 {
			return all, nil
		}
		cursor = info.Cursor
	}
}

// ---------------------------------------------------------------- Worker scripts

type Module struct {
	Name        string
	ContentType string
	Data        []byte
}

type ScriptInfo struct {
	ID         string `json:"id"`
	CreatedOn  string `json:"created_on"`
	ModifiedOn string `json:"modified_on"`
	Etag       string `json:"etag"`
	UsageModel string `json:"usage_model"`
}

// UploadScript pushes a module worker via the multipart script upload API.
func (c *Client) UploadScript(ctx context.Context, accountID, script string, metadata []byte, modules []Module) (*ScriptInfo, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	mh := make(textproto.MIMEHeader)
	mh.Set("Content-Disposition", `form-data; name="metadata"`)
	mh.Set("Content-Type", "application/json")
	part, err := mw.CreatePart(mh)
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(metadata); err != nil {
		return nil, err
	}

	for _, m := range modules {
		mh := make(textproto.MIMEHeader)
		mh.Set("Content-Disposition", fmt.Sprintf("form-data; name=%q; filename=%q", m.Name, m.Name))
		mh.Set("Content-Type", m.ContentType)
		part, err := mw.CreatePart(mh)
		if err != nil {
			return nil, err
		}
		if _, err := part.Write(m.Data); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}

	var info ScriptInfo
	_, err = c.request(ctx, http.MethodPut,
		fmt.Sprintf("/accounts/%s/workers/scripts/%s", url.PathEscape(accountID), url.PathEscape(script)),
		buf.Bytes(), mw.FormDataContentType(), &info)
	if err != nil {
		return nil, err
	}
	return &info, nil
}

// ListScripts returns every worker on the account.
func (c *Client) ListScripts(ctx context.Context, accountID string) ([]ScriptInfo, error) {
	var out []ScriptInfo
	_, err := c.request(ctx, http.MethodGet,
		fmt.Sprintf("/accounts/%s/workers/scripts", url.PathEscape(accountID)), nil, "", &out)
	return out, err
}

// GetScript returns one worker, or nil when it is not deployed.
func (c *Client) GetScript(ctx context.Context, accountID, script string) (*ScriptInfo, error) {
	out, err := c.ListScripts(ctx, accountID)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].ID == script {
			return &out[i], nil
		}
	}
	return nil, nil
}

type ScriptSettings struct {
	CompatibilityDate  string           `json:"compatibility_date"`
	CompatibilityFlags []string         `json:"compatibility_flags"`
	Bindings           []map[string]any `json:"bindings"`
	MigrationTag       string           `json:"migration_tag"`
}

func (c *Client) GetScriptSettings(ctx context.Context, accountID, script string) (*ScriptSettings, error) {
	var s ScriptSettings
	_, err := c.request(ctx, http.MethodGet,
		fmt.Sprintf("/accounts/%s/workers/scripts/%s/settings", url.PathEscape(accountID), url.PathEscape(script)),
		nil, "", &s)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (c *Client) WorkersDevSubdomain(ctx context.Context, accountID string) (string, error) {
	var out struct {
		Subdomain string `json:"subdomain"`
	}
	_, err := c.request(ctx, http.MethodGet,
		fmt.Sprintf("/accounts/%s/workers/subdomain", url.PathEscape(accountID)), nil, "", &out)
	if err != nil {
		// 10007 = the account has never opened Workers & Pages, so no
		// subdomain exists yet. That is a missing URL, not a failure.
		var he *HTTPError
		if ok := asHTTPError(err, &he); ok && (he.Status == 404 || he.has(10007)) {
			return "", nil
		}
		return "", err
	}
	return out.Subdomain, nil
}

func (c *Client) SetWorkersDev(ctx context.Context, accountID, script string, enabled bool) error {
	body, _ := json.Marshal(map[string]bool{"enabled": enabled, "previews_enabled": enabled})
	_, err := c.request(ctx, http.MethodPost,
		fmt.Sprintf("/accounts/%s/workers/scripts/%s/subdomain", url.PathEscape(accountID), url.PathEscape(script)),
		body, "application/json", nil)
	return err
}

func asHTTPError(err error, target **HTTPError) bool {
	for err != nil {
		if he, ok := err.(*HTTPError); ok {
			*target = he
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
