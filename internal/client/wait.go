package client

import (
	"context"
	"errors"
	"time"
)

// Transitional states: a resource passes through them on its way to a stable state
// (ADR 0032). Stable states are active, error and deleted.
var transitional = map[string]bool{"creating": true, "updating": true, "deleting": true}

// IsTransitional is true while the controllers are still working on a resource.
func IsTransitional(state string) bool { return transitional[state] }

// Lifecycle is embedded in every project resource.
type Lifecycle struct {
	State         string `json:"state"`
	StatusMessage string `json:"status_message"`
}

// Life returns the lifecycle fields; it lets Wait read any resource.
func (l Lifecycle) Life() Lifecycle { return l }

// Stateful is any resource with a lifecycle state.
type Stateful interface{ Life() Lifecycle }

// Wait polls path until the resource leaves its transitional state. ready (may be nil)
// adds a condition, e.g. a VM's power state matching the one asked for. The caller sets
// the deadline on ctx.
func Wait[T Stateful](ctx context.Context, c *Client, path string, ready func(T) bool) (*T, error) {
	return wait(ctx, c, path, false, ready)
}

// WaitGone polls path until a deleted resource is gone (404 or state "deleted").
func WaitGone[T Stateful](ctx context.Context, c *Client, path string) error {
	_, err := wait[T](ctx, c, path, true, nil)
	return err
}

func wait[T Stateful](ctx context.Context, c *Client, path string, untilGone bool, ready func(T) bool) (*T, error) {
	last := ""
	for {
		var obj T
		err := c.Get(ctx, path, &obj)
		if err != nil {
			if untilGone && IsNotFound(err) {
				return nil, nil
			}
			if ctx.Err() != nil {
				return nil, &WaitTimeoutError{Path: path, State: last}
			}
			return nil, err
		}
		life := obj.Life()
		last = life.State
		if life.State == "error" {
			return &obj, &ResourceFailedError{Path: path, Message: life.StatusMessage}
		}
		if untilGone && life.State == "deleted" {
			return nil, nil
		}
		if !untilGone && !IsTransitional(life.State) && (ready == nil || ready(obj)) {
			return &obj, nil
		}
		if err := c.sleep(ctx, c.PollInterval); err != nil {
			return &obj, &WaitTimeoutError{Path: path, State: last}
		}
	}
}

// RetryConflict calls fn until it stops failing with a plain 409 Conflict, for at most
// window (or until ctx ends). Use it for calls that race a controller, e.g. attaching a
// volume that is still detaching, or deleting a subnet right after its last VM.
func (c *Client) RetryConflict(ctx context.Context, window time.Duration, fn func() error) error {
	deadline := time.Now().Add(window)
	for {
		err := fn()
		if err == nil || !IsConflict(err) || time.Now().After(deadline) {
			return err
		}
		if sleepErr := c.sleep(ctx, c.PollInterval); sleepErr != nil {
			return errors.Join(err, sleepErr)
		}
	}
}
