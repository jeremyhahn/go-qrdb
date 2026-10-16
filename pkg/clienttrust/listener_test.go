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

package clienttrust

import (
	"crypto/tls"
	"errors"
	"slices"
	"sync"
	"testing"
)

// These tests are ReassertListenerTLS's own proof, independent of any
// transport. Every WithTLS (REST, gRPC, Unix, MCP) and QUIC's
// quicOnlyTLSConfig compose with this ONE helper rather than hand-rolling
// the same clone-and-rewrap; a defect here would reach every listener at
// once (go-qrdb#560 review, B2-3).

func TestReassertListenerTLS_NoDynamicHook_AppliesToClone(t *testing.T) {
	base := &tls.Config{MinVersion: 0}
	got := ReassertListenerTLS(base, func(c *tls.Config) {
		c.MinVersion = tls.VersionTLS13
	})
	if got.MinVersion != tls.VersionTLS13 {
		t.Fatalf("MinVersion = %v, want TLS 1.3", got.MinVersion)
	}
	// base itself must be untouched: the override must never leak back onto
	// the config shared with every other listener.
	if base.MinVersion != 0 {
		t.Fatalf("base was mutated: MinVersion = %v", base.MinVersion)
	}
}

// TestReassertListenerTLS_DynamicHook_ReassertsOnTheHookResult is the TLS
// 1.2 downgrade proof: a dynamic GetConfigForClient hook (installed by
// Pool.Wire, shared across every client-facing listener) returns a config
// cloned from whatever base it closed over -- which, without this wrapping,
// would carry the WRONG MinVersion on any connection that takes the Key B
// widened-pool path, even though the static base was correctly floored.
func TestReassertListenerTLS_DynamicHook_ReassertsOnTheHookResult(t *testing.T) {
	// keyBPool stands in for the config Pool.configForClient returns: it
	// was cloned from the version of base captured when Wire ran, BEFORE
	// any listener-specific override -- so its MinVersion is the TLS 1.2
	// default a listener never got the chance to raise.
	keyBPool := &tls.Config{MinVersion: tls.VersionTLS12}
	base := &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
			return keyBPool, nil
		},
	}

	cfg := ReassertListenerTLS(base, func(c *tls.Config) {
		c.MinVersion = tls.VersionTLS13
	})
	if cfg.MinVersion != tls.VersionTLS13 {
		t.Fatalf("clone's own MinVersion = %v, want TLS 1.3", cfg.MinVersion)
	}
	if cfg.GetConfigForClient == nil {
		t.Fatal("expected the dynamic hook to be preserved (wrapped), not dropped")
	}

	got, err := cfg.GetConfigForClient(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("wrapped GetConfigForClient: %v", err)
	}
	if got == keyBPool {
		t.Fatal("the wrapped hook returned the dynamic hook's own config: reasserting on it writes into a config other listeners serve (go-qrdb#741)")
	}
	if keyBPool.MinVersion != tls.VersionTLS12 {
		t.Fatalf("the dynamic hook's own config was mutated: MinVersion = %v, want TLS 1.2 untouched", keyBPool.MinVersion)
	}
	if got.MinVersion != tls.VersionTLS13 {
		t.Fatalf("the config returned by the dynamic hook was NOT reasserted: MinVersion = %v, want TLS 1.3 -- this is the TLS 1.2 downgrade go-qrdb#560 review B2-3 measured", got.MinVersion)
	}
}

func TestReassertListenerTLS_WrappedHookPropagatesErrors(t *testing.T) {
	wantErr := errors.New("boom")
	base := &tls.Config{
		GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
			return nil, wantErr
		},
	}
	cfg := ReassertListenerTLS(base, func(c *tls.Config) { c.MinVersion = tls.VersionTLS13 })
	_, err := cfg.GetConfigForClient(&tls.ClientHelloInfo{})
	if err != wantErr {
		t.Fatalf("expected the dynamic hook's error to propagate unchanged, got %v", err)
	}
}

func TestReassertListenerTLS_WrappedHookNilConfigPassesThrough(t *testing.T) {
	// A pkg/clienttrust hook returns (nil, nil) to mean "keep the static
	// config unchanged" (no Key B provider installed yet, or a lookup
	// failure). The wrapper must not panic applying the override to nil.
	base := &tls.Config{
		GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
			return nil, nil
		},
	}
	cfg := ReassertListenerTLS(base, func(c *tls.Config) { c.MinVersion = tls.VersionTLS13 })
	got, err := cfg.GetConfigForClient(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil config to pass through unchanged, got %+v", got)
	}
}

// TestReassertListenerTLS_ListenersSharingOneHookKeepTheirOwnOverride is the
// composition an embedded node runs (go-qrdb#741): REST and QUIC wrap one
// shared config whose hook -- Pool.Wire's, while it grants no widening --
// returns the SAME config to both. Each listener must get its own override
// and the shared config must stay as the composer built it. Run under
// -race: the two listeners' handshakes run concurrently.
func TestReassertListenerTLS_ListenersSharingOneHookKeepTheirOwnOverride(t *testing.T) {
	httpProtos := []string{"h2", "http/1.1"}
	shared := &tls.Config{NextProtos: slices.Clone(httpProtos)}
	New().Wire(shared)

	rest := ReassertListenerTLS(shared, func(c *tls.Config) { c.MinVersion = tls.VersionTLS13 })
	quic := ReassertListenerTLS(shared, func(c *tls.Config) { c.NextProtos = []string{"qrdb-quic/3"} })

	const handshakes = 8
	restGot := make([]*tls.Config, handshakes)
	quicGot := make([]*tls.Config, handshakes)
	var wg sync.WaitGroup
	for i := range handshakes {
		wg.Go(func() { restGot[i], _ = rest.GetConfigForClient(&tls.ClientHelloInfo{}) })
		wg.Go(func() { quicGot[i], _ = quic.GetConfigForClient(&tls.ClientHelloInfo{}) })
	}
	wg.Wait()

	for i := range handshakes {
		if got := restGot[i]; got.MinVersion != tls.VersionTLS13 || !slices.Equal(got.NextProtos, httpProtos) {
			t.Fatalf("REST handshake %d: MinVersion %v, NextProtos %v; want TLS 1.3 and %v", i, got.MinVersion, got.NextProtos, httpProtos)
		}
		if got := quicGot[i]; !slices.Equal(got.NextProtos, []string{"qrdb-quic/3"}) {
			t.Fatalf("QUIC handshake %d: NextProtos %v, want [qrdb-quic/3]", i, got.NextProtos)
		}
	}
	if shared.MinVersion != 0 || !slices.Equal(shared.NextProtos, httpProtos) {
		t.Fatalf("shared config mutated: MinVersion %v, NextProtos %v", shared.MinVersion, shared.NextProtos)
	}
}
