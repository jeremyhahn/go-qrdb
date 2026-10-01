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

package kvstore

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// probeAnswerStore is a MultiGuardedBatchKVStore whose guarded batch answers
// with a fixed error, recording what it was asked.
type probeAnswerStore struct {
	*MemoryKVStore
	err    error
	guards []Guard
	writes []KV
}

func (s *probeAnswerStore) PutAllIfUnchanged(ctx context.Context, key string, value []byte, writes []KV) (bool, error) {
	return s.PutAllIfAllUnchanged(ctx, []Guard{{Key: key, Value: value}}, writes)
}

func (s *probeAnswerStore) PutAllIfAllUnchanged(_ context.Context, guards []Guard, writes []KV) (bool, error) {
	s.guards, s.writes = guards, writes
	return false, s.err
}

var _ MultiGuardedBatchKVStore = (*probeAnswerStore)(nil)

func TestProbeGuardedBatch(t *testing.T) {
	transient := errors.New("no leader yet")
	for _, tc := range []struct {
		name        string
		err         error
		wantCapable bool
		wantErr     error
	}{
		{name: "state machine evaluated the transaction", err: nil, wantCapable: true},
		{name: "state machine refused the capability", err: &GuardedBatchUnsupportedError{Err: errors.New("mvcc not configured")}, wantCapable: false},
		{name: "bare sentinel is equally definitive", err: ErrAtomicWriteUnsupported, wantCapable: false},
		{name: "inconclusive failure is returned, not read as absence", err: transient, wantErr: transient},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &probeAnswerStore{MemoryKVStore: NewMemoryKVStore(), err: tc.err}
			capable, err := ProbeGuardedBatch(context.Background(), s)
			if capable != tc.wantCapable {
				t.Errorf("capable = %v, want %v", capable, tc.wantCapable)
			}
			if !errors.Is(err, tc.wantErr) || (tc.wantErr == nil && err != nil) {
				t.Errorf("err = %v, want %v", err, tc.wantErr)
			}
			if len(s.writes) != 0 {
				t.Errorf("probe wrote %d keys, want a no-op", len(s.writes))
			}
			if len(s.guards) != 1 || s.guards[0].Key != GuardedBatchProbeKey || s.guards[0].Value != nil {
				t.Errorf("guards = %+v, want one absent-guard on GuardedBatchProbeKey", s.guards)
			}
		})
	}
}

func TestGuardedBatchUnsupportedError(t *testing.T) {
	cause := errors.New("service: statemachine: mvcc not configured")
	err := &GuardedBatchUnsupportedError{Err: cause}
	if !errors.Is(err, ErrAtomicWriteUnsupported) {
		t.Error("errors.Is(err, ErrAtomicWriteUnsupported) = false, want true")
	}
	if !errors.Is(err, cause) {
		t.Error("the backend's refusal must be reachable through Unwrap")
	}
	if !strings.Contains(err.Error(), cause.Error()) {
		t.Errorf("Error() = %q, want it to carry the cause", err.Error())
	}
	if errors.Is(err, ErrIndexUnsupported) {
		t.Error("must not match an unrelated sentinel")
	}
	bare := &GuardedBatchUnsupportedError{}
	if bare.Error() == "" || bare.Unwrap() != nil {
		t.Errorf("zero value: Error() = %q, Unwrap() = %v", bare.Error(), bare.Unwrap())
	}
}

func TestNotAppliedError(t *testing.T) {
	cause := errors.New("service: not leader for shard 1, leader unknown")
	err := &NotAppliedError{Err: cause}
	if !errors.Is(err, ErrNotApplied) || !errors.Is(err, cause) {
		t.Fatalf("errors.Is(ErrNotApplied)=%v, errors.Is(cause)=%v, want both true", errors.Is(err, ErrNotApplied), errors.Is(err, cause))
	}
	if errors.Is(err, ErrAtomicWriteUnsupported) {
		t.Error("a refusal that is not about capability must not read as unsupported")
	}
	if err.Error() != cause.Error() {
		t.Errorf("Error() = %q, want the cause's message unchanged", err.Error())
	}
	if (&NotAppliedError{}).Error() != ErrNotApplied.Error() {
		t.Error("zero value must still describe itself")
	}
}

func TestGuardedBatchUnsupportedError_IsNotApplied(t *testing.T) {
	if !errors.Is(&GuardedBatchUnsupportedError{}, ErrNotApplied) {
		t.Error("an unsupported capability wrote nothing, so it must match ErrNotApplied")
	}
}

// TestProbeGuardedBatch_NotAppliedIsInconclusive: a proven-not-applied
// refusal that is not about capability (a NotLeader) is not an answer to the
// capability question.
func TestProbeGuardedBatch_NotAppliedIsInconclusive(t *testing.T) {
	refusal := &NotAppliedError{Err: errors.New("not leader")}
	s := &probeAnswerStore{MemoryKVStore: NewMemoryKVStore(), err: refusal}
	capable, err := ProbeGuardedBatch(context.Background(), s)
	if capable || !errors.Is(err, refusal) {
		t.Fatalf("ProbeGuardedBatch = (%v, %v), want (false, the refusal)", capable, err)
	}
}
