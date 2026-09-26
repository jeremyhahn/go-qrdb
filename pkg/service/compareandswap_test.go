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
	"bytes"
	"context"
	stderrors "errors"
	"testing"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// The service boundary for CompareAndSwap, on both implementations that carry
// it.
//
// KVService and MVCCKVService hold identical copies of this method, and every
// step between them and the state machine was unverified: which command is
// proposed, how ResultSwapped and ResultNotSwapped are read, whether an
// encoded application error in result.Data is surfaced or swallowed, and how a
// proposal failure is translated. Engine and protocol tests cover the ends;
// nothing covered the join (go-qrdb#56).
//
// Both are driven through one table because the requirement is that they agree
// — two copies of a method is how the two drift apart.
type swapService interface {
	CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error)
}

func swapServices(host Host) map[string]swapService {
	return map[string]swapService{
		"KVService":     NewKVService(host, 1).(swapService),
		"MVCCKVService": NewMVCCKVService(host, 1).(swapService),
	}
}

func TestServices_CompareAndSwap(t *testing.T) {
	ctx := context.Background()

	t.Run("proposes the encoded swap and reports it swapped", func(t *testing.T) {
		for name := range swapServices(&mockHost{}) {
			t.Run(name, func(t *testing.T) {
				var proposed []byte
				host := &mockHost{
					syncProposeFn: func(_ context.Context, shardID uint64, cmd []byte) (sm.Result, error) {
						proposed = append([]byte(nil), cmd...)
						if shardID != 1 {
							t.Errorf("proposed to shard %d, want 1", shardID)
						}
						return sm.Result{Value: protocol.ResultSwapped}, nil
					},
				}

				swapped, err := swapServices(host)[name].CompareAndSwap(ctx, "k", []byte("old"), []byte("new"))
				if err != nil || !swapped {
					t.Fatalf("CompareAndSwap = (%v, %v), want (true, nil)", swapped, err)
				}

				// The command must be the guarded swap carrying both operands,
				// not a plain put: a put would apply unconditionally and the
				// guard would be gone with nothing reporting its absence.
				if len(proposed) == 0 {
					t.Fatal("nothing was proposed")
				}
				if proposed[0] != protocol.OpCompareAndSwap {
					t.Fatalf("proposed opcode %d, want OpCompareAndSwap (%d)",
						proposed[0], protocol.OpCompareAndSwap)
				}
				key, expected, next, derr := protocol.DecodeCompareAndSwap(proposed[1:])
				if derr != nil {
					t.Fatalf("the proposed command does not decode: %v", derr)
				}
				if string(key) != "k" || !bytes.Equal(expected, []byte("old")) || !bytes.Equal(next, []byte("new")) {
					t.Errorf("decoded (%q, %q, %q), want (%q, %q, %q)",
						key, expected, next, "k", "old", "new")
				}
			})
		}
	})

	t.Run("reports a refused swap without an error", func(t *testing.T) {
		for name := range swapServices(&mockHost{}) {
			t.Run(name, func(t *testing.T) {
				host := &mockHost{
					syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
						return sm.Result{Value: protocol.ResultNotSwapped}, nil
					},
				}

				swapped, err := swapServices(host)[name].CompareAndSwap(ctx, "k", []byte("stale"), []byte("new"))
				// A refused guard is an ANSWER. Reporting it as an error would
				// make every contended swap look like a fault, and the quota
				// wrapper above reads the two differently.
				if err != nil {
					t.Fatalf("a refused swap returned an error: %v", err)
				}
				if swapped {
					t.Error("a refused swap reported success")
				}
			})
		}
	})

	t.Run("does not read an unrelated result as swapped", func(t *testing.T) {
		for name := range swapServices(&mockHost{}) {
			t.Run(name, func(t *testing.T) {
				host := &mockHost{
					syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
						// A state machine that applied something else entirely.
						return sm.Result{Value: 999}, nil
					},
				}

				swapped, err := swapServices(host)[name].CompareAndSwap(ctx, "k", []byte("old"), []byte("new"))
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if swapped {
					t.Error("a result that is not ResultSwapped was reported as a completed swap")
				}
			})
		}
	})

	t.Run("surfaces an encoded application error", func(t *testing.T) {
		for name := range swapServices(&mockHost{}) {
			t.Run(name, func(t *testing.T) {
				host := &mockHost{
					syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
						// The state machine encodes an application error in
						// Data rather than failing the apply, which is what
						// keeps Raft advancing. Swallowing it here would report
						// a swap that never happened as merely "not swapped".
						return sm.Result{
							Value: protocol.ResultSwapped,
							Data:  protocol.EncodeCapabilityError("subsystem not configured"),
						}, nil
					},
				}

				swapped, err := swapServices(host)[name].CompareAndSwap(ctx, "k", []byte("old"), []byte("new"))
				if err == nil {
					t.Fatal("an encoded application error was swallowed")
				}
				var notConfigured *CapabilityUnsupportedError
				if !stderrors.As(err, &notConfigured) {
					t.Fatalf("error is %T (%v), want *CapabilityUnsupportedError", err, err)
				}
				if swapped {
					t.Error("an application error reported a completed swap")
				}
			})
		}
	})

	t.Run("translates a NotLeader proposal failure", func(t *testing.T) {
		for name := range swapServices(&mockHost{}) {
			t.Run(name, func(t *testing.T) {
				host := &mockHost{
					syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
						return sm.Result{}, &quicraft.NotLeaderError{ShardID: 1, LeaderID: 2}
					},
				}

				swapped, err := swapServices(host)[name].CompareAndSwap(ctx, "k", []byte("old"), []byte("new"))
				if err == nil {
					t.Fatal("a proposal failure was reported as a completed call")
				}
				var notLeader *NotLeaderError
				if !stderrors.As(err, &notLeader) {
					t.Fatalf("error is %T (%v), want *NotLeaderError — a caller that "+
						"cannot see the leader cannot retry against it", err, err)
				}
				if swapped {
					t.Error("a failed proposal reported a completed swap")
				}
			})
		}
	})

	t.Run("wraps a generic proposal failure", func(t *testing.T) {
		for name := range swapServices(&mockHost{}) {
			t.Run(name, func(t *testing.T) {
				boom := stderrors.New("consensus unavailable")
				host := &mockHost{
					syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
						return sm.Result{}, boom
					},
				}

				swapped, err := swapServices(host)[name].CompareAndSwap(ctx, "k", []byte("old"), []byte("new"))
				if err == nil {
					t.Fatal("a proposal failure was reported as a completed call")
				}
				if !stderrors.Is(err, boom) {
					t.Errorf("error %v does not wrap the underlying failure", err)
				}
				if swapped {
					t.Error("a failed proposal reported a completed swap")
				}
			})
		}
	})

	t.Run("refuses a key the length field cannot describe", func(t *testing.T) {
		for name := range swapServices(&mockHost{}) {
			t.Run(name, func(t *testing.T) {
				proposed := false
				host := &mockHost{
					syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
						proposed = true
						return sm.Result{Value: protocol.ResultSwapped}, nil
					},
				}

				// The key length field is two bytes; this key cannot be framed.
				oversized := string(make([]byte, 65536))
				swapped, err := swapServices(host)[name].CompareAndSwap(ctx, oversized, []byte("old"), []byte("new"))

				if err == nil {
					t.Fatal("an unframeable key was accepted")
				}
				var encodeErr *EncodeRequestError
				if !stderrors.As(err, &encodeErr) {
					t.Fatalf("error is %T (%v), want *EncodeRequestError", err, err)
				}
				if swapped {
					t.Error("a rejected encode reported a completed swap")
				}
				if proposed {
					t.Error("a command that could not be encoded reached consensus")
				}
			})
		}
	})
}
