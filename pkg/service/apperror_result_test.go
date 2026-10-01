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

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// The reading half of the contract pinned on the producing side by
// pkg/statemachine's TestResolveApplyError_NotConfiguredIsEncodedAsADecodableAppError.
//
// The two halves cannot be asserted against each other in one test —
// pkg/service imports pkg/statemachine, so a test in pkg/statemachine cannot
// import back — and protocol.EncodeCapabilityError is the contract between them. Each
// side is therefore pinned against protocol, which is the thing they actually
// share (go-qrdb#62).
func TestCheckResultError_DecodesANotConfiguredResult(t *testing.T) {
	notConfigured := &statemachine.ServiceNotConfiguredError{Service: "transaction"}
	result := sm.Result{Data: protocol.EncodeCapabilityError(notConfigured.Error())}

	err := checkResultError(result)
	if err == nil {
		t.Fatal("an encoded application error read as a successful result; the " +
			"caller sees Succeeded=false with a nil error, which reads as a failed " +
			"compare rather than a misconfiguration")
	}
	var smErr *CapabilityUnsupportedError
	if !errors.As(err, &smErr) {
		t.Fatalf("error = %T (%v), want *CapabilityUnsupportedError", err, err)
	}
}

// And the reason the prefix matters: the same text WITHOUT it is not an error
// at all to this reader. This is what the two producing sites used to emit, and
// it is why the misconfiguration surfaced as an ordinary negative result.
func TestCheckResultError_AnUnprefixedPayloadIsNotRecognised(t *testing.T) {
	notConfigured := &statemachine.ServiceNotConfiguredError{Service: "transaction"}
	result := sm.Result{Data: []byte(notConfigured.Error())}

	if err := checkResultError(result); err != nil {
		t.Fatalf("checkResultError = %v; an unprefixed payload is indistinguishable "+
			"from result data, which is exactly the defect — this test documents the "+
			"reader's contract, so a change that made it lenient would be caught here",
			err)
	}
}

// An ordinary successful result is untouched.
func TestCheckResultError_APlainResultIsNotAnError(t *testing.T) {
	if err := checkResultError(sm.Result{Value: 1}); err != nil {
		t.Fatalf("checkResultError on a successful result = %v, want nil", err)
	}
}

// notConfiguredHost answers every proposal the way a state machine missing a
// subsystem does: a nil Raft error, with the failure encoded in the result.
func notConfiguredHost(service string) *mockHost {
	payload := protocol.EncodeCapabilityError((&statemachine.ServiceNotConfiguredError{Service: service}).Error())
	return &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{Data: payload}, nil
		},
		proposeWithModeFn: func(_ context.Context, _ uint64, _ []byte, _ writemode.Mode) (sm.Result, error) {
			return sm.Result{Data: payload}, nil
		},
	}
}

// The encoding is only half the fix. Every service method that PROPOSES has to
// read the result, and several discarded it — so a missing subsystem returned a
// nil error and the caller was told the write succeeded.
//
// That is a fail-open API contract violation, and it is the reachable half:
// checkResultError being correct means nothing at a call site that never calls
// it (go-qrdb#62).
func TestServices_ANotConfiguredSubsystemIsNotReportedAsSuccess(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		call func(Host) error
	}{
		{"IndexService.CreateIndex", func(h Host) error {
			return NewIndexService(h, 1).CreateIndex(ctx, "idx", "field", IndexConfig{})
		}},
		{"IndexService.DropIndex", func(h Host) error {
			return NewIndexService(h, 1).DropIndex(ctx, "idx")
		}},
		{"MVCCKVService.Delete", func(h Host) error {
			return NewMVCCKVService(h, 1).Delete(ctx, "k")
		}},
		{"MVCCKVService.Compact", func(h Host) error {
			return NewMVCCKVService(h, 1).Compact(ctx, 1)
		}},
		{"KVService.Delete", func(h Host) error {
			return NewKVService(h, 1).Delete(ctx, "k")
		}},
		{"MVCCKVService.DeleteWithMode", func(h Host) error {
			return NewMVCCKVService(h, 1).DeleteWithMode(ctx, "k", writemode.Sync)
		}},
		{"KVService.PutWithMode", func(h Host) error {
			return NewKVService(h, 1).PutWithMode(ctx, "k", []byte("v"), writemode.Sync)
		}},
		{"KVService.DeleteWithMode", func(h Host) error {
			return NewKVService(h, 1).DeleteWithMode(ctx, "k", writemode.Sync)
		}},
		{"HostProposerAdapter.SyncPropose", func(h Host) error {
			return NewHostProposerAdapter(h).SyncPropose(ctx, 1, []byte("cmd"))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call(notConfiguredHost("mvcc"))
			if err == nil {
				t.Fatal("a proposal against a missing subsystem returned nil; the " +
					"caller is told the write succeeded when nothing was applied")
			}
			var smErr *CapabilityUnsupportedError
			if !errors.As(err, &smErr) {
				t.Fatalf("error = %T (%v), want *CapabilityUnsupportedError", err, err)
			}
		})
	}
}

// And the other direction, so the new check cannot turn an ordinary successful
// proposal into a failure.
func TestServices_APlainProposalResultIsStillSuccess(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{Value: 1}, nil
		},
	}
	if err := NewIndexService(host, 1).DropIndex(context.Background(), "idx"); err != nil {
		t.Fatalf("DropIndex over a healthy state machine = %v, want nil", err)
	}
	if err := NewHostProposerAdapter(host).SyncPropose(context.Background(), 1, []byte("cmd")); err != nil {
		t.Fatalf("adapter SyncPropose over a healthy state machine = %v, want nil", err)
	}
}

// The import path has TWO branches over the same encoded error and they must
// behave differently, so one test each. Both branches previously counted a
// chunk that applied nothing as imported.
func TestBatchService_ImportDoesNotCountAnUnappliedChunkAsImported(t *testing.T) {
	ctx := context.Background()
	ops := [][]byte{[]byte("a"), []byte("b")}
	keys := []string{"k1", "k2"}
	rows := []int{1, 2}

	t.Run("transactional returns the error", func(t *testing.T) {
		svc := NewBatchService(notConfiguredHost("mvcc"), 1).(*BatchService)
		res, err := svc.proposeImportOps(ctx, &ImportResult{Total: len(ops)}, ops, keys, rows, true)
		if err == nil {
			t.Fatal("a transactional import against a missing subsystem returned nil; " +
				"the caller is told every row imported")
		}
		var smErr *CapabilityUnsupportedError
		if !errors.As(err, &smErr) {
			t.Fatalf("error = %T (%v), want *CapabilityUnsupportedError", err, err)
		}
		if res != nil && res.Succeeded != 0 {
			t.Fatalf("Succeeded = %d, want 0 — nothing was applied", res.Succeeded)
		}
	})

	t.Run("non-transactional fails the chunk's rows", func(t *testing.T) {
		svc := NewBatchService(notConfiguredHost("mvcc"), 1).(*BatchService)
		res, err := svc.proposeImportOps(ctx, &ImportResult{Total: len(ops)}, ops, keys, rows, false)
		if err != nil {
			t.Fatalf("non-transactional import returned %v; it reports per row instead", err)
		}
		if res.Succeeded != 0 {
			t.Fatalf("Succeeded = %d, want 0 — the chunk applied nothing", res.Succeeded)
		}
		if res.Failed != len(ops) {
			t.Fatalf("Failed = %d, want %d — every row in the chunk failed", res.Failed, len(ops))
		}
		if len(res.Errors) != len(ops) {
			t.Fatalf("got %d per-row errors, want %d", len(res.Errors), len(ops))
		}
		for i, e := range res.Errors {
			if e.Key != keys[i] || e.Row != rows[i] {
				t.Fatalf("error %d = %+v, want key %q row %d", i, e, keys[i], rows[i])
			}
		}
	})
}

// A healthy host still imports, so the new check cannot fail an import that
// actually applied.
func TestBatchService_ImportStillSucceedsOverAHealthyHost(t *testing.T) {
	ops := [][]byte{[]byte("a"), []byte("b")}
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{Value: 1}, nil
		},
	}
	svc := NewBatchService(host, 1).(*BatchService)
	res, err := svc.proposeImportOps(context.Background(), &ImportResult{Total: len(ops)},
		ops, []string{"k1", "k2"}, []int{1, 2}, false)
	if err != nil {
		t.Fatalf("import over a healthy host = %v, want nil", err)
	}
	if res.Succeeded != len(ops) || res.Failed != 0 {
		t.Fatalf("Succeeded=%d Failed=%d, want %d and 0", res.Succeeded, res.Failed, len(ops))
	}
}
