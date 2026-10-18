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

func TestInitializeRefusalCapture_DisarmedKeepsNothing(t *testing.T) {
	address := refusingServer(t, http.StatusUnauthorized, nil, `{"error":"later"}`)
	c := newInitializeRefusalCapture(http.DefaultTransport)
	c.disarm()
	req, err := http.NewRequest(http.MethodPost, address, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := c.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_ = resp.Body.Close()
	if got := c.Refusal(); got != nil {
		t.Errorf("Refusal = %v after disarm, want nil", got)
	}
}

func TestInitializeRefusalCapture_BodyIsHandedOn(t *testing.T) {
	const body = `{"error":"refused"}`
	address := refusingServer(t, http.StatusUnauthorized, nil, body)
	c := newInitializeRefusalCapture(http.DefaultTransport)
	req, err := http.NewRequest(http.MethodPost, address, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := c.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	got, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(got) != body {
		t.Errorf("body = %q, want %q", got, body)
	}
	if c.Refusal() == nil {
		t.Error("Refusal = nil, want the 401 kept")
	}
}

func TestInitializeRefusal_PlainTextBodyIsTheMessage(t *testing.T) {
	resp := &http.Response{Status: "401 Unauthorized", Header: http.Header{}}
	err := initializeRefusal(wire.CodeUnauthenticated, resp, []byte("unauthorized\n")).err
	if !strings.Contains(err.Error(), "unauthenticated: unauthorized") {
		t.Errorf("err = %q, want the plain body as the message", err)
	}
	err = initializeRefusal(wire.CodeUnauthenticated, resp, nil).err
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
