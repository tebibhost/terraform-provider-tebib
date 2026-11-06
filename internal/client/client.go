// Package client is a small HTTP client for the TebibHost API (/api/v1).
//
// It follows the CLI (platform/cli/src/tebib_cli/client.py):
//
//   - Auth: "Authorization: Bearer <key id>.<secret>". The key ID starts with TBK (access
//     key) or TBS (role session).
//   - Errors: the API's error body {"error": {"code", "message", "request_id"}} becomes an
//     *APIError.
//   - Creates send an Idempotency-Key, one per logical call, reused on retries.
//   - GET, PUT, PATCH and keyed creates are retried on connection errors and 502/503/504.
//     Any call is retried on 429, after Retry-After.
//   - List follows next_cursor. Wait polls a resource until it settles.
package client

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// APIPrefix is the path every API call starts with.
const APIPrefix = "/api/v1"

// DefaultEndpoint is the public API.
const DefaultEndpoint = "https://cloud.tebibhost.com"

var keyPrefixes = []string{"TBK", "TBS"}

// Config builds a Client.
type Config struct {
	Endpoint        string
	AccessKeyID     string
	SecretAccessKey string
	UserAgent       string
	// HTTPClient defaults to one with a 60 s timeout.
	HTTPClient *http.Client
	// Retries is the number of attempts for a retryable call (default 4).
	Retries int
	// Sleep waits between retries and polls; tests make it instant.
	Sleep func(ctx context.Context, d time.Duration) error
	// PollInterval is the wait between polls in Wait (default 2 s).
	PollInterval time.Duration
}

// Client talks to one TebibHost endpoint with one set of credentials.
type Client struct {
	base         string
	token        string
	userAgent    string
	http         *http.Client
	retries      int
	sleep        func(ctx context.Context, d time.Duration) error
	PollInterval time.Duration
}

// ValidateKeyID checks an access key ID the way the CLI does.
func ValidateKeyID(keyID string) error {
	ok := false
	for _, p := range keyPrefixes {
		if strings.HasPrefix(keyID, p) {
			ok = true
		}
	}
	if !ok || strings.Contains(keyID, ".") {
		return fmt.Errorf("%q is not a TebibHost access key ID (they start with TBK or TBS)", keyID)
	}
	return nil
}

// New checks the config and returns a Client.
func New(cfg Config) (*Client, error) {
	endpoint := strings.TrimRight(cfg.Endpoint, "/")
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("endpoint %q is not a URL like https://cloud.tebibhost.com", cfg.Endpoint)
	}
	if cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" {
		return nil, errors.New("no credentials: set access_key_id and secret_access_key " +
			"(or TEBIB_ACCESS_KEY_ID and TEBIB_SECRET_ACCESS_KEY, or a CLI profile)")
	}
	if err := ValidateKeyID(cfg.AccessKeyID); err != nil {
		return nil, err
	}
	c := &Client{
		base:         endpoint + APIPrefix,
		token:        cfg.AccessKeyID + "." + cfg.SecretAccessKey,
		userAgent:    cfg.UserAgent,
		http:         cfg.HTTPClient,
		retries:      cfg.Retries,
		sleep:        cfg.Sleep,
		PollInterval: cfg.PollInterval,
	}
	if c.http == nil {
		c.http = &http.Client{Timeout: 60 * time.Second}
	}
	if c.retries <= 0 {
		c.retries = 4
	}
	if c.sleep == nil {
		c.sleep = sleepCtx
	}
	if c.PollInterval <= 0 {
		c.PollInterval = 2 * time.Second
	}
	if c.userAgent == "" {
		c.userAgent = "terraform-provider-tebib"
	}
	return c, nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Request describes one API call.
type Request struct {
	Method string
	Path   string // after /api/v1, e.g. /projects/p-1/vms
	Query  url.Values
	Body   any
	// Idempotent marks a create the server de-duplicates by Idempotency-Key, so it is safe
	// to retry. Other POSTs are sent once (except on 429).
	Idempotent bool
}

// Do sends one call and decodes the JSON answer into out (if out is not nil).
func (c *Client) Do(ctx context.Context, r Request, out any) error {
	var payload []byte
	if r.Body != nil {
		var err error
		payload, err = json.Marshal(r.Body)
		if err != nil {
			return fmt.Errorf("encoding the request: %w", err)
		}
	}
	key := ""
	if r.Method == http.MethodPost && r.Idempotent {
		key = newKey()
	}
	retryable := r.Method == http.MethodGet || r.Method == http.MethodPut ||
		r.Method == http.MethodPatch || key != ""
	target := c.base + r.Path
	if len(r.Query) > 0 {
		target += "?" + r.Query.Encode()
	}
	for attempt := 1; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, r.Method, target, bodyReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", c.userAgent)
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil || !retryable || attempt >= c.retries {
				return fmt.Errorf("%s %s: %w", r.Method, r.Path, err)
			}
			if err := c.sleep(ctx, backoff(attempt)); err != nil {
				return err
			}
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		if readErr != nil {
			return fmt.Errorf("%s %s: reading the answer: %w", r.Method, r.Path, readErr)
		}
		if attempt < c.retries {
			if resp.StatusCode == http.StatusTooManyRequests {
				if err := c.sleep(ctx, retryAfter(resp, attempt)); err != nil {
					return err
				}
				continue
			}
			if retryable && (resp.StatusCode == 502 || resp.StatusCode == 503 || resp.StatusCode == 504) {
				if err := c.sleep(ctx, backoff(attempt)); err != nil {
					return err
				}
				continue
			}
		}
		if resp.StatusCode >= 400 {
			return errorFrom(resp, data)
		}
		if out == nil || len(bytes.TrimSpace(data)) == 0 {
			return nil
		}
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("%s %s: the API returned something this provider doesn't understand "+
				"(is the provider out of date?): %w", r.Method, r.Path, err)
		}
		return nil
	}
}

func bodyReader(b []byte) io.Reader {
	if b == nil {
		return nil
	}
	return bytes.NewReader(b)
}

func backoff(attempt int) time.Duration {
	d := time.Second << (attempt - 1)
	if d > 8*time.Second {
		d = 8 * time.Second
	}
	return d
}

func retryAfter(resp *http.Response, attempt int) time.Duration {
	if s := resp.Header.Get("Retry-After"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n >= 0 {
			d := time.Duration(n) * time.Second
			if d > 30*time.Second {
				d = 30 * time.Second
			}
			return d
		}
	}
	return backoff(attempt)
}

func newKey() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Get fetches one resource.
func (c *Client) Get(ctx context.Context, path string, out any) error {
	return c.Do(ctx, Request{Method: http.MethodGet, Path: path}, out)
}

// Create sends a create the server de-duplicates by Idempotency-Key.
func (c *Client) Create(ctx context.Context, path string, body, out any) error {
	return c.Do(ctx, Request{Method: http.MethodPost, Path: path, Body: body, Idempotent: true}, out)
}

// Post sends a POST that is not safe to repeat (IAM creates, actions).
func (c *Client) Post(ctx context.Context, path string, body, out any) error {
	return c.Do(ctx, Request{Method: http.MethodPost, Path: path, Body: body}, out)
}

// Patch changes fields of a resource.
func (c *Client) Patch(ctx context.Context, path string, body, out any) error {
	return c.Do(ctx, Request{Method: http.MethodPatch, Path: path, Body: body}, out)
}

// Put replaces a sub-resource (security groups of a VM, group membership).
func (c *Client) Put(ctx context.Context, path string, body, out any) error {
	return c.Do(ctx, Request{Method: http.MethodPut, Path: path, Body: body}, out)
}

// Delete deletes a resource. A 404 is returned as an error; callers decide.
func (c *Client) Delete(ctx context.Context, path string, out any) error {
	return c.Do(ctx, Request{Method: http.MethodDelete, Path: path}, out)
}

// GetList fetches a plain (not paginated) JSON array.
func GetList[T any](ctx context.Context, c *Client, path string, query url.Values) ([]T, error) {
	var items []T
	err := c.Do(ctx, Request{Method: http.MethodGet, Path: path, Query: query}, &items)
	return items, err
}

type page[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

// List follows next_cursor and returns every item of a paginated list.
func List[T any](ctx context.Context, c *Client, path string, query url.Values) ([]T, error) {
	var all []T
	q := url.Values{}
	for k, v := range query {
		q[k] = v
	}
	q.Set("limit", "200")
	for {
		var p page[T]
		if err := c.Do(ctx, Request{Method: http.MethodGet, Path: path, Query: q}, &p); err != nil {
			return nil, err
		}
		all = append(all, p.Items...)
		if p.NextCursor == nil || *p.NextCursor == "" {
			return all, nil
		}
		q.Set("cursor", *p.NextCursor)
	}
}

// Find returns the first item of a paginated list that matches.
func Find[T any](ctx context.Context, c *Client, path string, query url.Values, match func(T) bool) (*T, error) {
	items, err := List[T](ctx, c, path, query)
	if err != nil {
		return nil, err
	}
	for i := range items {
		if match(items[i]) {
			return &items[i], nil
		}
	}
	return nil, nil
}
