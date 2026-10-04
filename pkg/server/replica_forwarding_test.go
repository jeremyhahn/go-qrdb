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
	"crypto/x509"
	"log/slog"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
)

// TestReplicaForwarding_ListenersTrustTheForwardingPool: the listeners'
// forwarding configuration carries the locator and timeout given, and
// verifies targets against the pool REST leader forwarding trusts.
func TestReplicaForwarding_ListenersTrustTheForwardingPool(t *testing.T) {
	pool := x509.NewCertPool()
	locator := replicaforward.NewLocator(nil, replicaforward.NewLeaderCache(), "a:1", nil)
	s := &Server{}
	for _, opt := range []ServerOption{WithCACertPool(pool), WithReplicaForwarding(locator, 3*time.Second)} {
		opt(&s.opts)
	}
	cfg, ok := s.replicaForwarding()
	if !ok || cfg.Locator != locator || cfg.RootCAs != pool || cfg.Timeout != 3*time.Second {
		t.Fatalf("replicaForwarding = %+v, %v; want the locator, the pool and 3s", cfg, ok)
	}
}

// TestReplicaForwarding_NoneWithoutTheOptionOrAPool: a server not told to
// forward configures no listener to, and neither does one with nothing to
// verify a target against.
func TestReplicaForwarding_NoneWithoutTheOptionOrAPool(t *testing.T) {
	if _, ok := (&Server{}).replicaForwarding(); ok {
		t.Fatal("a server without WithReplicaForwarding forwards")
	}
	s := &Server{logger: slog.New(slog.DiscardHandler)}
	WithReplicaForwarding(replicaforward.NewLocator(nil, replicaforward.NewLeaderCache(), "a:1", nil), time.Second)(&s.opts)
	if _, ok := s.replicaForwarding(); ok {
		t.Fatal("a server with no CA pool forwards")
	}
}
