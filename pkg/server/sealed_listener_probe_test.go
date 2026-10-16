// Copyright (c) 2026 Jeremy Hahn
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

package server

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// initializedReadProbe is a sealed barrier that counts its Initialized reads.
type initializedReadProbe struct {
	reads int
	err   error
}

func (*initializedReadProbe) IsSealed() bool { return true }

func (p *initializedReadProbe) Initialized(ctx context.Context) (bool, error) {
	p.reads++
	if _, ok := ctx.Deadline(); !ok {
		return false, errors.New("unbounded read")
	}
	return true, p.err
}

// TestNewSealedWindowListenerProbe_ReadsInitializedOnce: building the probe
// reads the barrier's Initialized once, under a deadline, so the sealed
// window's status answers from memory; a failed read does not stop the build.
func TestNewSealedWindowListenerProbe_ReadsInitializedOnce(t *testing.T) {
	for _, readErr := range []error{nil, errors.New("store unavailable")} {
		barrier := &initializedReadProbe{err: readErr}
		if p := NewSealedWindowListenerProbe(barrier, nil); p == nil || !p.IsSealed() {
			t.Fatalf("read error %v: probe = %v, want one that reports sealed", readErr, p)
		}
		if barrier.reads != 1 {
			t.Errorf("read error %v: Initialized read %d times at build, want 1", readErr, barrier.reads)
		}
	}
}

// TestNewSealedWindowListenerProbe_PrimesTheBarrier: on a real barrier the
// read at build leaves its initialized state known, so barrier status on the
// sealed window needs no store read.
func TestNewSealedWindowListenerProbe_PrimesTheBarrier(t *testing.T) {
	ctx := context.Background()
	sw, err := seal.NewSoftwareStrategy(ctx, []byte("test-master-key-32bytes!abcdefgh"))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	barrier, err := seal.NewStorageBarrier(slog.New(slog.DiscardHandler), seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: "root-key", PreferenceOrder: []seal.StrategyID{seal.StrategyPassphrase}}, sw)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	t.Cleanup(func() { _ = barrier.Close() })
	if _, known := barrier.InitializedCached(); known {
		t.Fatal("a new barrier already knows whether it is initialized")
	}
	NewSealedWindowListenerProbe(barrier, nil)
	if initialized, known := barrier.InitializedCached(); !known || initialized {
		t.Errorf("InitializedCached after the probe = (%v, %v), want (false, true)", initialized, known)
	}
}

type fixedSealedProbe struct{ sealed bool }

func (p fixedSealedProbe) IsSealed() bool { return p.sealed }

// A probe built over a barrier reports its sealed state, and with no ceremony
// core there is no enrollment to keep open.
func TestNewSealedWindowListenerProbe_FollowsTheBarrier(t *testing.T) {
	for _, sealed := range []bool{true, false} {
		p := NewSealedWindowListenerProbe(fixedSealedProbe{sealed: sealed}, nil)
		if p.IsSealed() != sealed {
			t.Errorf("IsSealed() = %v, want %v", p.IsSealed(), sealed)
		}
		if p.EnrollmentOpen(context.Background()) {
			t.Error("enrollment reported open with no ceremony core")
		}
		p.Admit(true)()
		p.Admit(false)()
	}
}

// Without a barrier probe there is nothing to observe, so the probe fails
// closed: unsealed, enrollment closed, and every listener gate refuses.
func TestNewSealedWindowListenerProbe_NilBarrierFailsClosed(t *testing.T) {
	p := NewSealedWindowListenerProbe(nil, nil)
	if p == nil {
		t.Fatal("nil probe returned")
	}
	if p.IsSealed() || p.EnrollmentOpen(context.Background()) {
		t.Fatal("a probe with no barrier admitted a request")
	}
}
