package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func noSleep(ctx context.Context, _ time.Duration) error { return ctx.Err() }

func newTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(Config{
		Endpoint: srv.URL, AccessKeyID: "TBKTEST", SecretAccessKey: "s3cret",
		Sleep: noSleep, PollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNewValidatesConfig(t *testing.T) {
	cases := []Config{
		{Endpoint: "not a url", AccessKeyID: "TBKX", SecretAccessKey: "s"},
		{AccessKeyID: "", SecretAccessKey: "s"},
		{AccessKeyID: "TBKX", SecretAccessKey: ""},
		{AccessKeyID: "AKIA123", SecretAccessKey: "s"},
		{AccessKeyID: "TBKX.secret", SecretAccessKey: "s"},
	}
	for _, cfg := range cases {
		if _, err := New(cfg); err == nil {
			t.Errorf("New(%+v): want an error", cfg)
		}
	}
	if _, err := New(Config{AccessKeyID: "TBSsession", SecretAccessKey: "s"}); err != nil {
		t.Errorf("a role session key should be accepted: %v", err)
	}
}

func TestBearerTokenAndUserAgent(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer TBKTEST.s3cret" {
			t.Errorf("Authorization = %q", got)
		}
		if !strings.HasPrefix(r.URL.Path, "/api/v1/") {
			t.Errorf("path %q lacks /api/v1", r.URL.Path)
		}
		if r.Header.Get("User-Agent") == "" {
			t.Error("no User-Agent")
		}
		_, _ = w.Write([]byte(`{"id":"p-1","name":"web","state":"active"}`))
	})
	var p Project
	if err := c.Get(context.Background(), "/projects/p-1", &p); err != nil {
		t.Fatal(err)
	}
	if p.ID != "p-1" || p.State != "active" {
		t.Fatalf("decoded %+v", p)
	}
}

func TestErrorBodyBecomesAPIError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", "hdr-id")
		w.WriteHeader(409)
		_, _ = w.Write([]byte(`{"error":{"code":"QuotaExceeded","message":"No more VMs","request_id":"req-1","quota":"vms"}}`))
	})
	err := c.Get(context.Background(), "/x", nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("want *APIError, got %v", err)
	}
	if apiErr.Status != 409 || apiErr.Code != "QuotaExceeded" || apiErr.RequestID != "req-1" || apiErr.Extra["quota"] != "vms" {
		t.Fatalf("got %+v", apiErr)
	}
	if IsConflict(err) {
		t.Error("QuotaExceeded is not a plain conflict")
	}
	if !strings.Contains(err.Error(), "No more VMs") {
		t.Errorf("message lost: %v", err)
	}
}

func TestNonJSONError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		_, _ = w.Write([]byte("<html>nope</html>"))
	})
	err := c.Get(context.Background(), "/x", nil)
	if !IsNotFound(err) {
		t.Fatalf("want not found, got %v", err)
	}
	var apiErr *APIError
	errors.As(err, &apiErr)
	if apiErr.Code != "HTTP404" {
		t.Errorf("code %q", apiErr.Code)
	}
}

func TestCreateSendsOneIdempotencyKeyAcrossRetries(t *testing.T) {
	var mu sync.Mutex
	var keys []string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		if len(keys) < 3 {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(202)
		_, _ = w.Write([]byte(`{"id":"vpc-1"}`))
	})
	var v Vpc
	if err := c.Create(context.Background(), "/projects/p/vpcs", VpcIn{Name: "a"}, &v); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 3 || keys[0] == "" || keys[0] != keys[1] || keys[1] != keys[2] {
		t.Fatalf("keys %q", keys)
	}
}

func TestPlainPostIsNotRetriedOn503(t *testing.T) {
	calls := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Idempotency-Key") != "" {
			t.Error("a plain POST should not send an Idempotency-Key")
		}
		w.WriteHeader(503)
	})
	err := c.Post(context.Background(), "/orgs/o/roles", map[string]string{}, nil)
	if err == nil || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestDeleteIsNotRetried(t *testing.T) {
	calls := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(502)
	})
	if err := c.Delete(context.Background(), "/x", nil); err == nil || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestRateLimitedIsRetriedForAnyMethod(t *testing.T) {
	calls := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
			return
		}
		w.WriteHeader(204)
	})
	if err := c.Delete(context.Background(), "/x", nil); err != nil || calls != 2 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestListFollowsCursor(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("limit") != "200" {
			t.Errorf("limit %q", r.URL.Query().Get("limit"))
		}
		if r.URL.Query().Get("vpc_id") != "vpc-1" {
			t.Errorf("filter lost: %q", r.URL.RawQuery)
		}
		switch r.URL.Query().Get("cursor") {
		case "":
			_, _ = w.Write([]byte(`{"items":[{"id":"a"},{"id":"b"}],"next_cursor":"c2"}`))
		case "c2":
			_, _ = w.Write([]byte(`{"items":[{"id":"c"}],"next_cursor":null}`))
		default:
			t.Errorf("cursor %q", r.URL.Query().Get("cursor"))
		}
	})
	items, err := List[Subnet](context.Background(), c, "/projects/p/subnets", map[string][]string{"vpc_id": {"vpc-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || items[2].ID != "c" {
		t.Fatalf("items %+v", items)
	}
}

func TestWaitUntilActiveAndReady(t *testing.T) {
	polls := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		polls++
		state, power := "creating", "stopped"
		if polls >= 2 {
			state = "active"
		}
		if polls >= 3 {
			power = "running"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "vm-1", "state": state, "power_state": power})
	})
	vm, err := Wait(context.Background(), c, "/projects/p/vms/vm-1", func(v VM) bool { return v.PowerState == "running" })
	if err != nil {
		t.Fatal(err)
	}
	if vm.PowerState != "running" || polls != 3 {
		t.Fatalf("vm %+v after %d polls", vm, polls)
	}
}

func TestWaitErrorState(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"vm-1","state":"error","status_message":"no capacity"}`))
	})
	_, err := Wait[VM](context.Background(), c, "/v", nil)
	var failed *ResourceFailedError
	if !errors.As(err, &failed) || failed.Message != "no capacity" {
		t.Fatalf("got %v", err)
	}
}

func TestWaitGone(t *testing.T) {
	polls := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		polls++
		if polls < 3 {
			_, _ = w.Write([]byte(`{"id":"vm-1","state":"deleting"}`))
			return
		}
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"error":{"code":"NotFound","message":"gone"}}`))
	})
	if err := WaitGone[VM](context.Background(), c, "/v"); err != nil || polls != 3 {
		t.Fatalf("polls=%d err=%v", polls, err)
	}
}

func TestWaitTimeout(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"vm-1","state":"creating"}`))
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	c.sleep = sleepCtx
	_, err := Wait[VM](ctx, c, "/v", nil)
	var timeout *WaitTimeoutError
	if !errors.As(err, &timeout) || timeout.State != "creating" {
		t.Fatalf("got %v", err)
	}
}

func TestRetryConflict(t *testing.T) {
	calls := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {})
	err := c.RetryConflict(context.Background(), time.Minute, func() error {
		calls++
		if calls < 3 {
			return &APIError{Status: 409, Code: "Conflict", Message: "still detaching"}
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	calls = 0
	err = c.RetryConflict(context.Background(), time.Minute, func() error {
		calls++
		return &APIError{Status: 409, Code: "QuotaExceeded"}
	})
	if err == nil || calls != 1 {
		t.Fatalf("quota errors must not be retried: calls=%d", calls)
	}
}

func TestPaths(t *testing.T) {
	if got := ProjectPath("p-1", "vms", "vm-1", "start"); got != "/projects/p-1/vms/vm-1/start" {
		t.Error(got)
	}
	if got := OrgPath("o-1", "policies", "a/b"); got != "/orgs/o-1/policies/a%2Fb" {
		t.Error(got)
	}
}
