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
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/config"
)

// fakeSealedProbe is a deterministic SealedWindowProbe for tests.
type fakeSealedProbe struct {
	sealed atomic.Bool
}

func (p *fakeSealedProbe) IsSealed() bool { return p.sealed.Load() }

// listenerTestConfig returns a config with every protocol server
// disabled so ServeSealedWindow needs no ports or TLS material.
func listenerTestConfig() *config.Config {
	cfg := config.DefaultConfig()
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false
	cfg.Protocols.MySQL = false
	cfg.Protocols.PostgreSQL = false
	return cfg
}

// TestServeSealedWindow_NilProbe verifies the typed error when no probe
// is supplied — without one the listener could never observe unseal.
func TestServeSealedWindow_NilProbe(t *testing.T) {
	err := ServeSealedWindow(context.Background(), listenerTestConfig(), nil)
	if _, ok := err.(*NilSealedWindowProbeError); !ok {
		t.Fatalf("want *NilSealedWindowProbeError, got %T: %v", err, err)
	}
}

// TestServeSealedWindow_UnsealedReturnsImmediately verifies the loop
// exits with nil as soon as the probe reports unsealed.
func TestServeSealedWindow_UnsealedReturnsImmediately(t *testing.T) {
	probe := &fakeSealedProbe{} // zero value: unsealed
	if err := ServeSealedWindow(context.Background(), listenerTestConfig(), probe); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestServeSealedWindow_CanceledContext verifies that context
// cancellation while sealed returns *SealedWindowCanceledError.
func TestServeSealedWindow_CanceledContext(t *testing.T) {
	probe := &fakeSealedProbe{}
	probe.sealed.Store(true)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := ServeSealedWindow(ctx, listenerTestConfig(), probe)
	canceled, ok := err.(*SealedWindowCanceledError)
	if !ok {
		t.Fatalf("want *SealedWindowCanceledError, got %T: %v", err, err)
	}
	if canceled.Unwrap() == nil {
		t.Error("canceled error must wrap the context error")
	}
}

// TestServeSealedWindow_ObservesUnsealTransition verifies the poll loop
// notices a seal→unseal transition and returns nil.
func TestServeSealedWindow_ObservesUnsealTransition(t *testing.T) {
	probe := &fakeSealedProbe{}
	probe.sealed.Store(true)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- ServeSealedWindow(ctx, listenerTestConfig(), probe)
	}()

	probe.sealed.Store(false)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("unexpected error after unseal: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("ServeSealedWindow did not observe the unseal transition")
	}
}

// TestSealedWindowErrorStrings exercises the error formatting for both
// wrapped and bare variants.
func TestSealedWindowErrorStrings(t *testing.T) {
	if (&SealedWindowStartError{}).Error() == "" {
		t.Error("SealedWindowStartError must have a message")
	}
	if (&SealedWindowStartError{Err: context.Canceled}).Unwrap() != context.Canceled {
		t.Error("SealedWindowStartError must unwrap its cause")
	}
	if (&SealedWindowCanceledError{}).Error() == "" {
		t.Error("SealedWindowCanceledError must have a message")
	}
	if (&NilSealedWindowProbeError{}).Error() == "" {
		t.Error("NilSealedWindowProbeError must have a message")
	}
}
