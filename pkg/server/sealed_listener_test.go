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

	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
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

// soWriteCeremony is a ceremony core double whose HoldsSealedWindow can be
// parked, as the real core's is while Initialize holds its lock across the
// system SO record write, and then answers what the write left behind.
type soWriteCeremony struct {
	held   atomic.Bool
	park   atomic.Pointer[pollHold]
	polled chan struct{}
}

func (c *soWriteCeremony) EnrollmentOpen(context.Context) bool { return c.held.Load() }

func (c *soWriteCeremony) HoldsSealedWindow(context.Context) bool {
	if h := c.park.Swap(nil); h != nil {
		close(h.parked)
		<-h.release
	}
	select {
	case c.polled <- struct{}{}:
	default:
	}
	return c.held.Load()
}

// TestServeSealedWindow_HoldsTheWindowAcrossAFailedSOWrite pins go-qrdb#1579
// Codex round 1 P1 (3): a poll that reads the barrier unsealed while the
// ceremony's SO record write is in flight waits for that write; when it
// fails the window stays up and admits the retried CeremonyInitialize, and
// it closes only once the SO handoff is durable.
func TestServeSealedWindow_HoldsTheWindowAcrossAFailedSOWrite(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	probe := &fakeSealedProbe{}
	probe.sealed.Store(true)
	core := &soWriteCeremony{polled: make(chan struct{})}
	core.held.Store(true)

	done := make(chan error, 1)
	go func() {
		done <- ServeSealedWindow(ctx, listenerTestConfig(), probe, withSealedWindowCeremony(core))
	}()
	nextPoll := func(stage string) {
		t.Helper()
		select {
		case <-core.polled:
		case err := <-done:
			t.Fatalf("%s: the sealed window closed (%v)", stage, err)
		case <-ctx.Done():
			t.Fatalf("%s: the poll loop never polled", stage)
		}
	}

	// Initialize unsealed the barrier and is writing the SO record: the
	// next poll reads the barrier unsealed and waits on the ceremony.
	hold := &pollHold{parked: make(chan struct{}), release: make(chan struct{})}
	core.park.Store(hold)
	probe.sealed.Store(false)
	select {
	case <-hold.parked:
	case <-ctx.Done():
		t.Fatal("the poll never consulted the ceremony with the barrier unsealed")
	}
	// The write fails: the M-of-N handoff stays pending.
	close(hold.release)
	nextPoll("after the failed SO write")
	nextPoll("a later poll")

	listener := NewSealedWindowListenerProbe(probe, nil).(sealedWindowListenerProbe)
	listener.ceremony = core
	decision := sealedwindow.Decide(sealedwindow.Request{
		Sealed: listener.IsSealed(), Listener: true, Allowlisted: true,
		PreOperationalOnly: true, EnrollmentOpen: listener.EnrollmentOpen(ctx),
	})
	if decision != sealedwindow.Serve {
		t.Fatalf("the held window refuses the retried CeremonyInitialize: %v", decision)
	}

	// The retry records the SO: the window closes.
	core.held.Store(false)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ServeSealedWindow after the SO handoff: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("the window never closed after the SO handoff")
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
