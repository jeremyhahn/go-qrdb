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

package embedded

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestLockout_OneRawBackendTwoConsumersShareOneBudget: the same RAW backend
// handed to two consumers that each install the lockout -- the ceremony
// (SetPINBackend) and this transport (WithPINBackend) -- is decorated once,
// so wrong PINs spent through either count against one set of counters: the
// free failures split across the two still lock both out.
func TestLockout_OneRawBackendTwoConsumersShareOneBudget(t *testing.T) {
	raw := pintest.NewSOPINBackend(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	strategy, err := seal.NewShamirStrategy(2, 2)
	if err != nil {
		t.Fatal(err)
	}
	barrier, err := seal.NewStorageBarrier(logger, seal.NewMemoryBackend(), seal.BarrierConfig{
		RootKeyPath: "root-key", Shamir: &seal.ShamirConfig{Threshold: 2, TotalShares: 2},
	}, strategy)
	if err != nil {
		t.Fatal(err)
	}
	core, err := ceremony.NewCeremonyService(&ceremony.CeremonyConfig{DataDir: t.TempDir()}, barrier, logger)
	if err != nil {
		t.Fatal(err)
	}
	core.SetPINBackend(raw)
	tr, err := New(newMockKVService(), &mockAdminService{}, WithPINBackend(raw), WithBarrierService(&mockBarrierService{}))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	viaCeremony := auth.SOPINFreeFailures / 2
	for i := 0; i < viaCeremony; i++ {
		if err := core.AuthorizeSOPIN(ctx, "wrong-pin-0000"); !errors.Is(err, pin.ErrPINInvalid) {
			t.Fatalf("ceremony wrong PIN %d = %v", i+1, err)
		}
	}
	for i := viaCeremony; i < auth.SOPINFreeFailures; i++ {
		if _, err := tr.BarrierGenerateRecoveryKeys(ctx, &transport.BarrierGenerateRecoveryKeysRequest{Threshold: 2, SOPin: "wrong-pin-0000"}); !errors.Is(err, pin.ErrPINInvalid) {
			t.Fatalf("transport wrong PIN %d = %v", i+1, err)
		}
	}
	if err := core.AuthorizeSOPIN(ctx, pintest.ThrottledSOPIN); !errors.Is(err, pin.ErrPINLocked) {
		t.Fatalf("the right PIN after the free failures split across two consumers = %v, want pin.ErrPINLocked", err)
	}
	if pin.NewLockoutBackend(raw) != pin.NewLockoutBackend(raw) {
		t.Fatal("decorating one raw backend twice made two decorations")
	}
}
