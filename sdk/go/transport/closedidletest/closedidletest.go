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

// Package closedidletest reproduces, deterministically, a server closing an
// idle keep-alive connection at the moment a client hands a request to it:
// the race net/http reports as "http: server closed idle connection".
//
// A Server serves a compare-and-swap that applies at most once. A context
// from Arm makes the first request handed a reused connection close that
// connection from the server's side, and wait until the client's read loop
// has seen the close, before the request is written. The request then fails
// with net/http's own errServerClosedIdle, never having reached the server.
package closedidletest

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// Initial is the value the swap expects; Swapped is the one it stores.
const (
	Initial = "initial"
	Swapped = "swapped"
)

// Server is a test server with one compare-and-swap at "/cas": a POST whose
// body is Initial swaps the value to Swapped once and answers 200; any later
// one answers 409. GET "/warm" answers 204 and leaves a keep-alive
// connection in the client's idle pool.
type Server struct {
	srv *httptest.Server

	mu       sync.Mutex
	value    string
	applied  atomic.Int64
	closes   atomic.Int64
	received atomic.Int64
}

// New starts a Server, closed when t ends.
func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{value: Initial}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /warm", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /cas", s.cas)
	mux.HandleFunc("POST /cas-drop", s.casDrop)
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

func (s *Server) cas(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if string(body) != s.value {
		http.Error(w, "compare failed", http.StatusConflict)
		return
	}
	s.value = Swapped
	s.applied.Add(1)
	w.WriteHeader(http.StatusOK)
}

// casDrop applies the swap like cas, then closes the connection without
// writing a response: the server committed the request and the client sees
// only a broken connection, an ambiguous failure that must not be resent.
func (s *Server) casDrop(w http.ResponseWriter, r *http.Request) {
	s.received.Add(1)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	if string(body) == s.value {
		s.value = Swapped
		s.applied.Add(1)
	}
	s.mu.Unlock()
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "connection cannot be hijacked", http.StatusInternalServerError)
		return
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		return
	}
	_ = conn.Close()
}

// Received is how many requests reached POST "/cas-drop".
func (s *Server) Received() int64 { return s.received.Load() }

// DropRequest returns a POST /cas-drop carrying Initial, bound to ctx: the
// server applies it and then closes the connection without answering. Its
// body is a strings.Reader, so http.NewRequest sets GetBody.
func (s *Server) DropRequest(t testing.TB, ctx context.Context) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.srv.URL+"/cas-drop", strings.NewReader(Initial))
	if err != nil {
		t.Fatalf("drop request: %v", err)
	}
	return req
}

// URL is the server's base URL.
func (s *Server) URL() string { return s.srv.URL }

// Applied is how many times the swap was applied.
func (s *Server) Applied() int64 { return s.applied.Load() }

// Closes is how many connections an armed context closed.
func (s *Server) Closes() int64 { return s.closes.Load() }

// Transport returns a fresh http.Transport to the server whose connections
// the armed hook can observe.
func (s *Server) Transport() *http.Transport {
	dialer := &net.Dialer{}
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			c, err := dialer.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			return &clientConn{Conn: c, closed: make(chan struct{})}, nil
		},
	}
}

// Warm sends GET /warm through rt and drains the answer, leaving its
// connection idle in rt's pool.
func (s *Server) Warm(t testing.TB, rt http.RoundTripper) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, s.srv.URL+"/warm", http.NoBody)
	if err != nil {
		t.Fatalf("warm request: %v", err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("warm: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

// SwapRequest returns a POST /cas carrying Initial, bound to ctx. Its body is
// a strings.Reader, so http.NewRequest sets GetBody.
func (s *Server) SwapRequest(t testing.TB, ctx context.Context) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.srv.URL+"/cas", strings.NewReader(Initial))
	if err != nil {
		t.Fatalf("swap request: %v", err)
	}
	return req
}

// Arm returns ctx with a client trace that, the first time a request bound
// to it is handed a reused connection, closes the server's side of every
// client connection and waits until the client's read loop has closed that
// connection in turn. The handed-out request has not been written yet, so
// net/http fails it with "http: server closed idle connection".
func (s *Server) Arm(ctx context.Context) context.Context {
	var once sync.Once
	return withGotConn(ctx, func(conn net.Conn, reused bool) {
		if !reused {
			return
		}
		once.Do(func() {
			c, ok := conn.(*clientConn)
			if !ok {
				return
			}
			s.srv.CloseClientConnections()
			<-c.closed
			s.closes.Add(1)
		})
	})
}

// clientConn notes its Close. A write after it succeeds and goes nowhere: the
// bytes of a request written onto a connection the server has closed are
// never read, which is what a socket whose peer has closed does with them.
type clientConn struct {
	net.Conn
	once   sync.Once
	closed chan struct{}
}

func (c *clientConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

func (c *clientConn) Write(p []byte) (int, error) {
	select {
	case <-c.closed:
		return len(p), nil
	default:
		return c.Conn.Write(p)
	}
}

func withGotConn(ctx context.Context, f func(conn net.Conn, reused bool)) context.Context {
	return httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) { f(info.Conn, info.Reused) },
	})
}
