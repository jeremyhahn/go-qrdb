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

package rest

import (
	"context"
	"crypto/tls"
	"testing"
	"time"

	quicgo "github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

// h3TicketCache is a client session cache that signals when a session ticket
// is stored, so the test resumes only once it holds one.
type h3TicketCache struct {
	tls.ClientSessionCache
	put chan struct{}
}

func (c *h3TicketCache) Put(key string, cs *tls.ClientSessionState) {
	c.ClientSessionCache.Put(key, cs)
	if cs == nil {
		return
	}
	select {
	case c.put <- struct{}{}:
	default:
	}
}

// TestHTTP3_ZeroRTTRefused: the REST server's HTTP/3 listener serves the same
// handlers as HTTP/2, state-changing ones included, and 0-RTT early data is
// replayable -- an on-path attacker can resend a captured flight and the
// server processes it again. quic-go's http3.Server accepts 0-RTT unless its
// QUIC configuration says otherwise, so a resumed client must be made to
// complete a full handshake first.
func TestHTTP3_ZeroRTTRefused(t *testing.T) {
	tlsCfg, pool := generateTestTLSConfig(t)
	srv := NewServer(&mockKVService{}, &mockAdminService{}, dualStackAddrOn(t, "127.0.0.1"), WithTLS(tlsCfg))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)

	cache := &h3TicketCache{ClientSessionCache: tls.NewLRUClientSessionCache(4), put: make(chan struct{}, 1)}
	clientCfg := &tls.Config{
		RootCAs:            pool,
		ServerName:         "localhost",
		NextProtos:         []string{http3.NextProtoH3},
		MinVersion:         tls.VersionTLS13,
		ClientSessionCache: cache,
	}
	quicCfg := &quicgo.Config{MaxIdleTimeout: 5 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	first, err := quicgo.DialAddr(ctx, srv.Addr(), clientCfg, quicCfg)
	if err != nil {
		t.Fatalf("first dial: %v", err)
	}
	select {
	case <-cache.put:
	case <-ctx.Done():
		t.Fatal("the HTTP/3 listener issued no session ticket")
	}
	_ = first.CloseWithError(0, "ticket obtained")

	early, err := quicgo.DialAddrEarly(ctx, srv.Addr(), clientCfg, quicCfg)
	if err != nil {
		t.Fatalf("early dial: %v", err)
	}
	defer func() { _ = early.CloseWithError(0, "done") }()
	select {
	case <-early.HandshakeComplete():
	case <-ctx.Done():
		t.Fatal("handshake did not complete")
	}
	state := early.ConnectionState()
	if !state.TLS.DidResume {
		t.Fatal("the second connection did not resume the session, so it proves nothing about 0-RTT")
	}
	if state.Used0RTT {
		t.Fatal("the HTTP/3 listener accepted 0-RTT application data, which an attacker can replay")
	}
}
