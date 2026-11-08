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

package server

import (
	"errors"
	"log/slog"
	"net"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/clientlimit"
	"github.com/jeremyhahn/go-qrdb/pkg/config"
)

func clientLimitServer(cfg config.ClientRateLimitConfig) *Server {
	c := &config.Config{}
	c.Transport.ClientRateLimit = cfg
	return &Server{config: c, logger: slog.New(slog.DiscardHandler)}
}

// TestOpenClientLimiter_SharedAndClosed: an enabled configuration opens ONE
// limiter, reopening keeps it (every transport shares the bucket), it
// enforces the configured burst, and closing releases it idempotently.
func TestOpenClientLimiter_SharedAndClosed(t *testing.T) {
	s := clientLimitServer(config.ClientRateLimitConfig{RatePerSecond: 1e-6, Burst: 1, TrustedProxies: []config.TrustedProxyConfig{{Address: "10.0.0.0/8", Header: "x-forwarded-for"}}})
	if err := s.openClientLimiter(); err != nil {
		t.Fatalf("open: %v", err)
	}
	l := s.clientLimit
	if l == nil {
		t.Fatal("enabled configuration opened no limiter")
	}
	if err := s.openClientLimiter(); err != nil || s.clientLimit != l {
		t.Fatalf("reopen: err %v, limiter replaced %v", err, s.clientLimit != l)
	}
	peer := &net.TCPAddr{IP: net.ParseIP("192.0.2.1"), Port: 1}
	if err := l.Admit(peer, nil); err != nil {
		t.Fatalf("first request: %v", err)
	}
	var rl *clientlimit.RateLimitedError
	if err := l.Admit(peer, nil); !errors.As(err, &rl) {
		t.Fatalf("over the burst: %v, want *RateLimitedError", err)
	}
	s.closeClientLimiter()
	if s.clientLimit != nil {
		t.Fatal("close left the limiter in place")
	}
	s.closeClientLimiter()
}

// TestOpenClientLimiter_DisabledAndInvalid: the default configuration opens
// nothing, and an invalid trusted proxy fails the open.
func TestOpenClientLimiter_DisabledAndInvalid(t *testing.T) {
	s := clientLimitServer(config.ClientRateLimitConfig{})
	if err := s.openClientLimiter(); err != nil || s.clientLimit != nil {
		t.Fatalf("disabled: err %v, limiter %v", err, s.clientLimit)
	}
	s = clientLimitServer(config.ClientRateLimitConfig{RatePerSecond: 1, Burst: 1, TrustedProxies: []config.TrustedProxyConfig{{Address: "not-an-address", Header: "forwarded"}}})
	if err := s.openClientLimiter(); err == nil || s.clientLimit != nil {
		t.Fatalf("invalid proxy: err %v, limiter %v", err, s.clientLimit)
	}
}
