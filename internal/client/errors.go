package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// APIError is an error answer from the API.
type APIError struct {
	Status    int
	Code      string
	Message   string
	RequestID string
	Extra     map[string]any
}

func (e *APIError) Error() string {
	msg := fmt.Sprintf("%s (HTTP %d): %s", e.Code, e.Status, e.Message)
	if e.RequestID != "" {
		msg += fmt.Sprintf(" [request %s]", e.RequestID)
	}
	return msg
}

func errorFrom(resp *http.Response, data []byte) error {
	e := &APIError{Status: resp.StatusCode, RequestID: resp.Header.Get("X-Request-ID")}
	var body struct {
		Error map[string]any `json:"error"`
	}
	if json.Unmarshal(data, &body) == nil && body.Error != nil {
		e.Code = fmt.Sprint(valueOr(body.Error["code"], "Error"))
		e.Message = fmt.Sprint(valueOr(body.Error["message"], ""))
		if rid, ok := body.Error["request_id"].(string); ok && rid != "" {
			e.RequestID = rid
		}
		for k, v := range body.Error {
			if k != "code" && k != "message" && k != "request_id" {
				if e.Extra == nil {
					e.Extra = map[string]any{}
				}
				e.Extra[k] = v
			}
		}
		return e
	}
	e.Code = fmt.Sprintf("HTTP%d", resp.StatusCode)
	text := strings.TrimSpace(string(data))
	if len(text) > 300 {
		text = text[:300]
	}
	if text == "" {
		text = http.StatusText(resp.StatusCode)
	}
	e.Message = text
	return e
}

func valueOr(v any, def string) any {
	if v == nil {
		return def
	}
	return v
}

// IsNotFound is true for a 404 answer.
func IsNotFound(err error) bool {
	var e *APIError
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}

// IsConflict is true for a plain 409 Conflict (not QuotaExceeded or InsufficientBalance,
// which waiting doesn't fix).
func IsConflict(err error) bool {
	var e *APIError
	return errors.As(err, &e) && e.Status == http.StatusConflict && e.Code == "Conflict"
}

// ResourceFailedError means a resource settled in state "error".
type ResourceFailedError struct {
	Path    string
	Message string
}

func (e *ResourceFailedError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = "no details"
	}
	return fmt.Sprintf("%s is in state error: %s", e.Path, msg)
}

// WaitTimeoutError means a resource didn't settle in time.
type WaitTimeoutError struct {
	Path  string
	State string
}

func (e *WaitTimeoutError) Error() string {
	return fmt.Sprintf("%s is still %q: timed out waiting for it to settle "+
		"(raise the timeouts block, or check the resource in the dashboard)", e.Path, e.State)
}
