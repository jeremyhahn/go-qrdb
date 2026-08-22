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
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// oversizedKey is one byte past what the two-byte key length field can
// describe, so every encoder that frames a key refuses it.
func oversizedKey() string { return strings.Repeat("k", protocol.MaxUint16Field+2) }

// refusingHost fails the test if a proposal is ever made. Nothing may reach the
// cluster once encoding has been refused.
func refusingHost(t *testing.T) *mockHost {
	t.Helper()
	return &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			t.Fatal("the host was asked to propose a command that could not be encoded")
			return sm.Result{}, nil
		},
	}
}

// A request that cannot be encoded is the CALLER's error, and the services must
// say so.
//
// Encoding used to be infallible. Now that the encoders refuse a field longer
// than its length field can describe, every call site had a new failure to
// report — and reported it as ProposeError, "the Raft proposal failed", for a
// proposal that was never attempted. The transports map ProposeError to gRPC
// Internal and HTTP 500, so an oversized key told the caller the cluster was
// broken: an operational alert fires, and a client retrying on 5xx retries an
// input that can never succeed (go-qrdb#52).
//
// Each subtest also proves the host was never called, because "never proposed"
// is the fact that makes this a client error rather than an infrastructure one.
func TestServices_AnUnencodableRequestIsAClientErrorAndNeverProposes(t *testing.T) {
	ctx := context.Background()
	long := oversizedKey()

	// qrdbFramed marks the calls whose command is built by THIS repo's
	// pkg/protocol encoders, so the cause is a *protocol.FieldTooLongError. The
	// base KV plane is framed by go-quicraft's kv encoders instead, which carry
	// their own equivalent bound and their own error type — the classification
	// as a client error is what both must share, not the cause type.
	for _, tc := range []struct {
		name       string
		qrdbFramed bool
		call       func(*testing.T, Host) error
	}{
		{"KVService.Put", false, func(_ *testing.T, h Host) error {
			return NewKVService(h, 1).Put(ctx, long, []byte("v"))
		}},
		{"KVService.Delete", false, func(_ *testing.T, h Host) error {
			return NewKVService(h, 1).Delete(ctx, long)
		}},
		{"KVService.PutIfAbsent", false, func(_ *testing.T, h Host) error {
			_, err := NewKVService(h, 1).PutIfAbsent(ctx, long, []byte("v"))
			return err
		}},
		{"MVCCKVService.Put", true, func(_ *testing.T, h Host) error {
			return NewMVCCKVService(h, 1).Put(ctx, long, []byte("v"))
		}},
		{"MVCCKVService.Delete", true, func(_ *testing.T, h Host) error {
			return NewMVCCKVService(h, 1).Delete(ctx, long)
		}},
		{"MVCCKVService.CompareAndDelete", true, func(_ *testing.T, h Host) error {
			_, err := NewMVCCKVService(h, 1).CompareAndDelete(ctx, long, []byte("v"))
			return err
		}},
		{"IndexService.CreateIndex", true, func(_ *testing.T, h Host) error {
			return NewIndexService(h, 1).CreateIndex(ctx, long, "field", IndexConfig{})
		}},
		{"IndexService.DropIndex", true, func(_ *testing.T, h Host) error {
			return NewIndexService(h, 1).DropIndex(ctx, long)
		}},
		// The time-series plane frames its metric in a TWO-byte field, so this
		// is reachable there where it is not on the transaction and batch
		// planes — see TestFramedPlanes_AcceptAKeyTheTwoBytePlanesRefuse.
		{"TimeSeriesService.Write/metric", true, func(_ *testing.T, h Host) error {
			return NewTimeSeriesService(h, 1).Write(ctx, long, 1, time.Unix(0, 0), nil)
		}},
		{"TimeSeriesService.Write/tag value", true, func(_ *testing.T, h Host) error {
			return NewTimeSeriesService(h, 1).Write(ctx, "m", 1, time.Unix(0, 0),
				map[string]string{"host": long})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call(t, refusingHost(t))
			if err == nil {
				t.Fatal("an unencodable request returned no error")
			}

			var encodeErr *EncodeRequestError
			if !errors.As(err, &encodeErr) {
				t.Fatalf("error = %T (%v), want *EncodeRequestError — a ProposeError "+
					"here maps to Internal/500 and reports a healthy cluster as broken",
					err, err)
			}

			var proposeErr *ProposeError
			if errors.As(err, &proposeErr) {
				t.Fatal("the error is still a ProposeError; no proposal was made, so " +
					"it must not claim one failed")
			}

			// The root cause stays reachable, so a caller can see WHICH field
			// was too long without parsing a message.
			if errors.Unwrap(err) == nil {
				t.Fatalf("the cause did not survive wrapping: %v", err)
			}
			if tc.qrdbFramed {
				var tooLong *protocol.FieldTooLongError
				if !errors.As(err, &tooLong) {
					t.Fatalf("cause = %v, want a *protocol.FieldTooLongError naming the field", err)
				}
			}
		})
	}
}

// The classification must not swallow real proposal failures: a request that
// encodes fine and fails at the cluster is still a ProposeError.
func TestServices_AGenuineProposalFailureIsStillAProposeError(t *testing.T) {
	sentinel := errors.New("raft is down")
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{}, sentinel
		},
	}

	err := NewMVCCKVService(host, 1).Put(context.Background(), "small", []byte("v"))
	if err == nil {
		t.Fatal("a failing proposal returned no error")
	}
	var encodeErr *EncodeRequestError
	if errors.As(err, &encodeErr) {
		t.Fatalf("a cluster failure was reported as a client error: %v", err)
	}
}

// Why there is no client-error subtest for the transaction and batch planes,
// recorded as a measurement rather than left as an absence someone reads as an
// oversight.
//
// Those encoders frame keys, values and elements in FOUR-byte length fields
// (EncodeCompare, EncodeOp, EncodeTransaction, EncodeBatch), so the smallest
// input that trips a guard there is 4 GiB — not constructible in a test, and
// not reachable by any caller that can hold its own request in memory. The
// encode-error wrapping on those paths is therefore consistency, not a fix for
// something a client can reach today, and a subtest asserting a client error
// there would be asserting a branch it cannot enter.
//
// What IS true and worth pinning: the same 65,537-byte key that the two-byte
// planes refuse is perfectly legal on the four-byte planes, and proposes.
func TestFramedPlanes_AcceptAKeyTheTwoBytePlanesRefuse(t *testing.T) {
	long := oversizedKey()
	proposed := 0
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			proposed++
			return sm.Result{Data: []byte{1}}, nil
		},
	}

	if _, err := NewTransactionService(host, 1).Execute(context.Background(),
		[]Compare{{Key: []byte(long), Target: CompareValue, Result: CompareEqual, Value: []byte("v")}},
		nil, nil); err != nil {
		t.Fatalf("a %d-byte key on the four-byte-framed transaction plane: %v", len(long), err)
	}
	if _, err := NewBatchService(host, 1).Execute(context.Background(),
		[]Op{{Type: OpPut, Key: []byte(long), Value: []byte("v")}}); err != nil {
		t.Fatalf("a %d-byte key on the four-byte-framed batch plane: %v", len(long), err)
	}

	if proposed != 2 {
		t.Fatalf("proposed %d times, want 2 — both planes must encode and propose "+
			"a key their length fields can describe", proposed)
	}
}
