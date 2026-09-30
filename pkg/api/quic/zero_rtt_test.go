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

package quic

import (
	"context"
	"crypto/tls"
	"testing"
	"time"

	quicgo "github.com/quic-go/quic-go"
)

// TestQUIC_ZeroRTTRefused: 0-RTT application data is replayable -- an
// on-path attacker can resend a captured early-data flight and the server
// processes it again -- and every frame this listener serves is a request,
// state-changing ones included. The listener used to accept 0-RTT; a resumed
// client must now always complete a full handshake before its data is
// accepted.
func TestQUIC_ZeroRTTRefused(t *testing.T) {
	env := startIntegrationServer(t, &mockKVService{}, &mockAdminService{})

	cache := &ticketSignalCache{ClientSessionCache: tls.NewLRUClientSessionCache(4), put: make(chan struct{}, 1)}
	clientCfg := env.clientCfg.Clone()
	clientCfg.ClientSessionCache = cache
	quicCfg := &quicgo.Config{MaxIdleTimeout: 5 * time.Second}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// First connection: a full handshake that leaves a session ticket in the
	// client's cache. The ticket arrives after the handshake, so keep the
	// connection up until it has.
	first, err := quicgo.DialAddr(ctx, env.srv.Addr(), clientCfg, quicCfg)
	if err != nil {
		t.Fatalf("first dial: %v", err)
	}
	stream, err := first.OpenStreamSync(ctx)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	_ = stream.Close()
	select {
	case <-cache.put:
	case <-ctx.Done():
		t.Fatal("the server issued no session ticket")
	}
	_ = first.CloseWithError(0, "ticket obtained")

	// Second connection: offer 0-RTT with the cached ticket.
	early, err := quicgo.DialAddrEarly(ctx, env.srv.Addr(), clientCfg, quicCfg)
	if err != nil {
		t.Fatalf("early dial: %v", err)
	}
	defer func() { _ = early.CloseWithError(0, "done") }()
	select {
	case <-early.HandshakeComplete():
	case <-ctx.Done():
		t.Fatal("handshake did not complete")
	}
	if !early.ConnectionState().TLS.DidResume {
		t.Fatal("the second connection did not resume the session, so it proves nothing about 0-RTT")
	}
	if early.ConnectionState().Used0RTT {
		t.Fatal("the QUIC listener accepted 0-RTT application data, which an attacker can replay")
	}
}

// ticketSignalCache is a client session cache that signals when a session
// ticket is stored, so the test resumes only once it holds one.
type ticketSignalCache struct {
	tls.ClientSessionCache
	put chan struct{}
}

func (c *ticketSignalCache) Put(key string, cs *tls.ClientSessionState) {
	c.ClientSessionCache.Put(key, cs)
	if cs == nil {
		return
	}
	select {
	case c.put <- struct{}{}:
	default:
	}
}
