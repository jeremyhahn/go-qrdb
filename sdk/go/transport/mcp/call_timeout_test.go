// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// callTimeoutTestTimeout is the transport timeout the stalled-server tests
// configure; callTimeoutTestBound is how long a test waits for the call to
// come back before declaring it unbounded. The gap is wide so a loaded host
// cannot turn a bounded call into a failure.
const (
	callTimeoutTestTimeout = 200 * time.Millisecond
	callTimeoutTestBound   = 10 * time.Second
)

// stalledServer serves MCP over HTTP with a qrdb_lock_acquire tool that never
// answers: it returns only when the request's context ends or the test ends.
// It is a server whose consensus has stalled, seen from the client.
func stalledServer(t *testing.T) *httptest.Server {
	t.Helper()
	release := make(chan struct{})
	srv := mcpserver.NewMCPServer("qrdb-stalled", "1.0.0", mcpserver.WithToolCapabilities(false))
	srv.AddTool(mcp.NewTool("qrdb_lock_acquire"), func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return mcp.NewToolResultText(`{}`), nil
		}
	})
	hs := httptest.NewServer(mcpserver.NewStreamableHTTPServer(srv))
	// Cleanups run last-registered first: release the handler, then close.
	t.Cleanup(hs.Close)
	t.Cleanup(func() { close(release) })
	return hs
}

// newTimeoutTransport builds an unconnected network transport against
// address with the given request timeout.
func newTimeoutTransport(t *testing.T, address string, timeout time.Duration) *Transport {
	t.Helper()
	tr, err := New(transport.WithAddress(address), transport.WithTimeout(timeout))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	return tr
}

// boundedCall runs call and fails the test if it does not return within
// callTimeoutTestBound, naming step.
func boundedCall(t *testing.T, step string, call func() error) (time.Duration, error) {
	t.Helper()
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- call() }()
	select {
	case err := <-done:
		return time.Since(start), err
	case <-time.After(callTimeoutTestBound):
		t.Fatalf("%s did not return within %v of a %v request timeout", step, callTimeoutTestBound, callTimeoutTestTimeout)
		return 0, nil
	}
}

func TestCallTool_StalledServerFailsAtTheRequestTimeout(t *testing.T) {
	hs := stalledServer(t)
	tr := newTimeoutTransport(t, hs.URL+"/mcp", callTimeoutTestTimeout)
	if err := tr.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	elapsed, err := boundedCall(t, "LockAcquire", func() error {
		_, err := tr.LockAcquire(context.Background(), &transport.LockAcquireRequest{Key: "k", TTL: 10})
		return err
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("LockAcquire against a stalled server: err = %v, want context.DeadlineExceeded", err)
	}
	if elapsed < callTimeoutTestTimeout {
		t.Fatalf("LockAcquire returned after %v, before the %v request timeout", elapsed, callTimeoutTestTimeout)
	}
}

func TestCallTool_CallerDeadlineEarlierThanTimeoutWins(t *testing.T) {
	hs := stalledServer(t)
	tr := newTimeoutTransport(t, hs.URL+"/mcp", time.Hour)
	if err := tr.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	_, err := boundedCall(t, "LockAcquire", func() error {
		ctx, cancel := context.WithTimeout(context.Background(), callTimeoutTestTimeout)
		defer cancel()
		_, err := tr.LockAcquire(ctx, &transport.LockAcquireRequest{Key: "k", TTL: 10})
		return err
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("LockAcquire with an earlier caller deadline: err = %v, want context.DeadlineExceeded", err)
	}
}

func TestConnect_StalledInitializeFailsAtTheRequestTimeout(t *testing.T) {
	release := make(chan struct{})
	hs := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	// Cleanups run last-registered first: release the handler, then close.
	t.Cleanup(hs.Close)
	t.Cleanup(func() { close(release) })
	tr := newTimeoutTransport(t, hs.URL+"/mcp", callTimeoutTestTimeout)

	_, err := boundedCall(t, "Connect", func() error {
		return tr.Connect(context.Background())
	})
	var connErr *transport.ConnectionError
	if !errors.As(err, &connErr) {
		t.Fatalf("Connect against a stalled initialize: err = %v, want *transport.ConnectionError", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Connect against a stalled initialize: err = %v, want context.DeadlineExceeded", err)
	}
}

func TestCallContext_PositiveTimeoutSetsADeadline(t *testing.T) {
	tr := &Transport{config: &transport.Config{Timeout: time.Minute}}
	before := time.Now()
	ctx, cancel := tr.callContext(context.Background())
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("callContext with a positive timeout returned a context without a deadline")
	}
	if deadline.Before(before.Add(time.Minute)) {
		t.Fatalf("deadline %v is earlier than the configured minute from %v", deadline, before)
	}
}

func TestCallContext_ZeroTimeoutLeavesTheContextUnbounded(t *testing.T) {
	tr := &Transport{config: &transport.Config{}}
	parent := context.Background()
	ctx, cancel := tr.callContext(parent)
	defer cancel()
	if ctx != parent {
		t.Fatal("callContext with a zero timeout replaced the caller's context")
	}
	if _, ok := ctx.Deadline(); ok {
		t.Fatal("callContext with a zero timeout added a deadline")
	}
}
