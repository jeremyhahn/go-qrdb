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
	"crypto/tls"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
)

// These tests close a regression pkg/clienttrust's dynamic client-auth trust
// pool would otherwise cause on this listener: GetConfigForClient's returned
// config REPLACES the connection's effective tls.Config wholesale, so a hook
// that clones from the pre-ALPN-pin config (the one shared with REST/gRPC/
// Unix/MCP) would serve the wrong ALPN list and fail every handshake that
// takes the dynamic path once a Key B login exists.

func TestQuicOnlyTLSConfig_PinsALPNWithNoDynamicHook(t *testing.T) {
	base := &tls.Config{NextProtos: []string{"h3", "h2", "http/1.1"}}
	got := quicOnlyTLSConfig(base)
	if len(got.NextProtos) != 1 || got.NextProtos[0] != wire.ProtocolV3 {
		t.Fatalf("NextProtos = %v, want [%s]", got.NextProtos, wire.ProtocolV3)
	}
	// base itself must be untouched: this listener's ALPN pin must never leak
	// back onto the config shared with REST/gRPC/Unix/MCP.
	if len(base.NextProtos) != 3 {
		t.Fatalf("base.NextProtos was mutated: %v", base.NextProtos)
	}
}

func TestQuicOnlyTLSConfig_WrapsDynamicHookAndReassertsALPN(t *testing.T) {
	keyBPool := &tls.Config{} // stand-in for a widened config pkg/clienttrust would return
	base := &tls.Config{
		NextProtos: []string{"h3", "h2", "http/1.1"}, // the shared, non-QUIC ALPN list
		GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
			return keyBPool, nil
		},
	}

	cfg := quicOnlyTLSConfig(base)
	if cfg.GetConfigForClient == nil {
		t.Fatal("expected the dynamic hook to be preserved (wrapped), not dropped")
	}

	got, err := cfg.GetConfigForClient(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("wrapped GetConfigForClient: %v", err)
	}
	if got != keyBPool {
		t.Fatal("expected the wrapped hook to return the SAME config the dynamic hook produced (not a fresh copy)")
	}
	if len(got.NextProtos) != 1 || got.NextProtos[0] != wire.ProtocolV3 {
		t.Fatalf("the config returned by the dynamic hook must have its ALPN re-pinned to %s, got %v",
			wire.ProtocolV3, got.NextProtos)
	}
}

func TestQuicOnlyTLSConfig_WrappedHookPropagatesErrors(t *testing.T) {
	wantErr := errors.New("boom")
	base := &tls.Config{
		GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
			return nil, wantErr
		},
	}
	cfg := quicOnlyTLSConfig(base)
	_, err := cfg.GetConfigForClient(&tls.ClientHelloInfo{})
	if err != wantErr {
		t.Fatalf("expected the dynamic hook's error to propagate unchanged, got %v", err)
	}
}

func TestQuicOnlyTLSConfig_WrappedHookNilConfigPassesThrough(t *testing.T) {
	// A pkg/clienttrust hook returns (nil, nil) to mean "keep the static
	// config unchanged" (no Key B provider installed yet, or a lookup
	// failure). The QUIC wrapper must not panic on a nil config.
	base := &tls.Config{
		GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
			return nil, nil
		},
	}
	cfg := quicOnlyTLSConfig(base)
	got, err := cfg.GetConfigForClient(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil config to pass through unchanged, got %+v", got)
	}
}
