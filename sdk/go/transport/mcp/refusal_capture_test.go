// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
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
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// refusingServer answers every request with status, the given headers and
// body, as the auth chain answers a refused initialize.
func refusingServer(t *testing.T, status int, header map[string]string, body string) string {
	t.Helper()
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for k, v := range header {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(hs.Close)
	return hs.URL
}

// connectRefused connects a transport to address and returns Connect's error.
func connectRefused(t *testing.T, address string) error {
	t.Helper()
	tr, err := New(transport.WithAddress(address), transport.WithTimeout(5*time.Second))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	err = tr.Connect(context.Background())
	if err == nil {
		t.Fatal("Connect succeeded, want the refusal")
	}
	return err
}

func TestConnect_401InitializeIsTheTypedUnauthenticatedRefusal(t *testing.T) {
	const msg = "auth/middleware: no valid credentials provided"
	address := refusingServer(t, http.StatusUnauthorized, map[string]string{
		"Content-Type":     "application/json",
		"WWW-Authenticate": `Bearer realm="mcp"`,
	}, `{"error":"`+msg+`"}`)

	err := connectRefused(t, address)
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("err = %T %v, want *transport.RequestError", err, err)
	}
	if reqErr.StatusCode != int(wire.CodeUnauthenticated) {
		t.Errorf("StatusCode = %d, want %d", reqErr.StatusCode, wire.CodeUnauthenticated)
	}
	if reqErr.Method != "initialize" {
		t.Errorf("Method = %q, want initialize", reqErr.Method)
	}
	if !strings.Contains(err.Error(), "unauthenticated: "+msg) {
		t.Errorf("err = %q, want the server's message", err)
	}
	var connErr *transport.ConnectionError
	if errors.As(err, &connErr) {
		t.Errorf("err is a ConnectionError, want the server's verdict: %v", err)
	}
}

func TestConnect_403InitializeIsTheTypedPermissionRefusal(t *testing.T) {
	const msg = "auth/middleware: user cannot authenticate: alice in tenant acme (lifecycle suspended)"
	address := refusingServer(t, http.StatusForbidden, map[string]string{
		"Content-Type": "application/json",
	}, `{"error":"`+msg+`"}`)

	err := connectRefused(t, address)
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("err = %T %v, want *transport.RequestError", err, err)
	}
	if reqErr.StatusCode != int(wire.CodePermissionDenied) {
		t.Errorf("StatusCode = %d, want %d", reqErr.StatusCode, wire.CodePermissionDenied)
	}
	if !strings.Contains(err.Error(), "permission denied: "+msg) {
		t.Errorf("err = %q, want the server's message", err)
	}
}

func TestConnect_RefusalCarriesRetryAfter(t *testing.T) {
	address := refusingServer(t, http.StatusForbidden, map[string]string{
		"Content-Type": "application/json",
		"Retry-After":  "3",
	}, `{"error":"refused"}`)

	var reqErr *transport.RequestError
	if err := connectRefused(t, address); !errors.As(err, &reqErr) {
		t.Fatalf("err = %T %v, want *transport.RequestError", err, err)
	}
	if reqErr.RetryAfter != 3*time.Second {
		t.Errorf("RetryAfter = %v, want 3s", reqErr.RetryAfter)
	}
}

func TestConnect_NonAuth4xxKeepsTheConnectionError(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusMethodNotAllowed} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			err := connectRefused(t, refusingServer(t, status, nil, "nope"))
			var connErr *transport.ConnectionError
			if !errors.As(err, &connErr) {
				t.Fatalf("err = %T %v, want *transport.ConnectionError", err, err)
			}
			var initErr *initializeError
			if !errors.As(err, &initErr) {
				t.Errorf("err = %v, want it to wrap initializeError", err)
			}
			var reqErr *transport.RequestError
			if errors.As(err, &reqErr) {
				t.Errorf("err = %v carries a RequestError for a non-auth %d", err, status)
			}
		})
	}
}

// roundTripRefused sends a POST to address through a refusalCapture with ctx.
func roundTripRefused(t *testing.T, ctx context.Context, address string) string {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, address, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := (&refusalCapture{next: http.DefaultTransport}).RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	got, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return string(got)
}

func TestRefusalCapture_RequestWithoutSlotKeepsNothing(t *testing.T) {
	const body = `{"error":"refused"}`
	address := refusingServer(t, http.StatusUnauthorized, nil, body)
	_, slot := withRefusalSlot(context.Background(), "qrdb_get")
	if got := roundTripRefused(t, context.Background(), address); got != body {
		t.Errorf("body = %q, want %q", got, body)
	}
	if got := slot.Refusal(); got != nil {
		t.Errorf("Refusal = %v for a request that did not carry the slot, want nil", got)
	}
}

func TestRefusalCapture_BodyIsHandedOn(t *testing.T) {
	const body = `{"error":"refused"}`
	address := refusingServer(t, http.StatusUnauthorized, nil, body)
	ctx, slot := withRefusalSlot(context.Background(), "qrdb_get")
	if got := roundTripRefused(t, ctx, address); got != body {
		t.Errorf("body = %q, want %q", got, body)
	}
	var reqErr *transport.RequestError
	if err := slot.Refusal(); !errors.As(err, &reqErr) || reqErr.Method != "qrdb_get" {
		t.Errorf("Refusal = %v, want the 401 kept as qrdb_get's RequestError", err)
	}
}

func TestRefusalCapture_SuccessKeepsNothing(t *testing.T) {
	address := refusingServer(t, http.StatusOK, nil, `{}`)
	ctx, slot := withRefusalSlot(context.Background(), "qrdb_get")
	_ = roundTripRefused(t, ctx, address)
	if got := slot.Refusal(); got != nil {
		t.Errorf("Refusal = %v after a 200, want nil", got)
	}
}

// TestCallTool_401AfterInitializeIsTheTypedRefusal: a server that answers
// initialize and refuses the caller's credential at tools/call -- what a
// node serving no-login tools does with a refused certificate -- yields the
// server's typed refusal, not mcp-go's bare authorization-required error.
func TestCallTool_401AfterInitializeIsTheTypedRefusal(t *testing.T) {
	const msg = "auth/middleware: client certificate is invalid or unverified"
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		switch {
		case strings.Contains(string(raw), `"initialize"`):
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":0,"result":{"protocolVersion":"2025-06-18","capabilities":{},"serverInfo":{"name":"qrdb","version":"test"}}}`)
		case strings.Contains(string(raw), `"tools/call"`):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":"`+msg+`"}`)
		default:
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	t.Cleanup(hs.Close)

	tr, err := New(transport.WithAddress(hs.URL), transport.WithTimeout(5*time.Second))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	if err := tr.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	_, err = tr.Get(context.Background(), "k")
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("err = %T %v, want *transport.RequestError", err, err)
	}
	if reqErr.StatusCode != int(wire.CodeUnauthenticated) {
		t.Errorf("StatusCode = %d, want %d", reqErr.StatusCode, wire.CodeUnauthenticated)
	}
	if !strings.Contains(err.Error(), msg) {
		t.Errorf("err = %q, want the server's message", err)
	}
}

func TestHTTPRefusal_PlainTextBodyIsTheMessage(t *testing.T) {
	resp := &http.Response{Status: "401 Unauthorized", Header: http.Header{}}
	err := httpRefusal("initialize", wire.CodeUnauthenticated, resp, []byte("unauthorized\n")).err
	if !strings.Contains(err.Error(), "unauthenticated: unauthorized") {
		t.Errorf("err = %q, want the plain body as the message", err)
	}
	err = httpRefusal("initialize", wire.CodeUnauthenticated, resp, nil).err
	if !strings.Contains(err.Error(), "401 Unauthorized") {
		t.Errorf("err = %q, want the status line for an empty body", err)
	}
}

func TestRetryAfterSeconds(t *testing.T) {
	future := time.Now().Add(90 * time.Second).UTC().Format(http.TimeFormat)
	cases := []struct {
		in       string
		min, max int64
	}{
		{"", 0, 0},
		{"5", 5, 5},
		{"-4", 0, 0},
		{"soon", 0, 0},
		{"Mon, 01 Jan 2001 00:00:00 GMT", 0, 0},
		{future, 85, 91},
	}
	for _, tc := range cases {
		if got := retryAfterSeconds(tc.in); got < tc.min || got > tc.max {
			t.Errorf("retryAfterSeconds(%q) = %d, want [%d, %d]", tc.in, got, tc.min, tc.max)
		}
	}
}
