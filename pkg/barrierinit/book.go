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

package barrierinit

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
)

// Prefix is where init records live in the system store, one per barrier:
// Prefix + barrier. It is a sibling of the cluster seal records and, like
// them, holds only public fields. "__sys:" is an owner-enumerated namespace,
// so a Scan of it is not filtered.
const Prefix = "__sys:cluster/barrier/init/"

// KV is the slice of the system store a Book and a Watcher need: the
// replicated system shard, or the standalone node's own store. Get and Scan
// must be linearizable reads. PutIf must be evaluated by the state machine --
// the existing guarded write -- so that replicas cannot diverge and two
// writers racing on one key cannot both win.
type KV interface {
	// Get returns the value under key and whether it exists.
	Get(ctx context.Context, key string) (value []byte, found bool, err error)
	// PutIf stores value under key only while key's value equals expected,
	// an absent key holding the empty value. A refused write is (false, nil).
	PutIf(ctx context.Context, key string, expected, value []byte) (stored bool, err error)
	// Scan returns every key and value under prefix.
	Scan(ctx context.Context, prefix string) (map[string][]byte, error)
}

// record is the stored value: every version, epoch 1 first, so that a joiner
// can verify the chain.
type record struct {
	Versions []InitStatement `json:"versions"`
}

func recordKey(b clusterseal.Barrier) string { return Prefix + string(b) }

func encodeRecord(versions []InitStatement) ([]byte, error) {
	return json.Marshal(record{Versions: versions})
}

// decodeRecord decodes key's value, refusing one whose versions are not b's
// dense epochs from 1.
func decodeRecord(key string, b clusterseal.Barrier, raw []byte) ([]InitStatement, error) {
	var r record
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, &CorruptRecordError{Key: key, Err: err}
	}
	if len(r.Versions) == 0 {
		return nil, &CorruptRecordError{Key: key, Reason: "record holds no versions"}
	}
	for i, v := range r.Versions {
		if v.Barrier != b || v.Epoch != uint64(i+1) {
			return nil, &CorruptRecordError{Key: key, Reason: "versions are not the barrier's epochs from 1"}
		}
	}
	return r.Versions, nil
}

// Book records barriers' init statements.
type Book struct {
	kv       KV
	required func() error
}

// NewBook returns a Book over kv. required returns nil once
// featuregate.PerNodeBarrierKeys is activated and the refusal (the composer's
// *featuregate.NotActiveError) before; nil is a node that gates nothing -- a
// standalone node -- and is active from the start.
func NewBook(kv KV, required func() error) *Book {
	return &Book{kv: kv, required: required}
}

func (b *Book) checkActive() error {
	if b.required == nil {
		return nil
	}
	return b.required()
}

// Claim records s as its barrier's first epoch, with a guarded
// create-if-absent: of two concurrent claims exactly one is stored, and the
// other gets *BarrierAlreadyInitializedError -- before any key exists. A
// retry of the stored statement succeeds, however it was signed again. s must
// verify against a.
func (b *Book) Claim(ctx context.Context, s InitStatement, a Anchors) error {
	if err := b.checkActive(); err != nil {
		return err
	}
	if s.Epoch != 1 {
		return &EpochConflictError{Barrier: s.Barrier, Epoch: s.Epoch}
	}
	if err := Verify(s, a); err != nil {
		return err
	}
	raw, err := encodeRecord([]InitStatement{s})
	if err != nil {
		return &StoreError{Op: "encode init record", Err: err}
	}
	stored, err := b.kv.PutIf(ctx, recordKey(s.Barrier), nil, raw)
	if err != nil {
		return &StoreError{Op: "claim init record", Err: err}
	}
	if stored {
		return nil
	}
	versions, err := b.Versions(ctx, s.Barrier)
	if err != nil {
		return err
	}
	if len(versions) > 0 && sameStatement(versions[0], s) {
		return nil
	}
	return &BarrierAlreadyInitializedError{Barrier: s.Barrier}
}

// Advance records s as the epoch after its barrier's current one, with a
// guarded compare-and-set over the record read: a statement for any other
// epoch, or one that lost a race to a rival for the same epoch, gets
// *EpochConflictError. A retry of the stored statement succeeds, however it
// was signed again. s must verify against a.
func (b *Book) Advance(ctx context.Context, s InitStatement, a Anchors) error {
	if err := b.checkActive(); err != nil {
		return err
	}
	if s.Epoch < 2 {
		return &EpochConflictError{Barrier: s.Barrier, Epoch: s.Epoch}
	}
	if err := Verify(s, a); err != nil {
		return err
	}
	versions, raw, err := b.load(ctx, s.Barrier)
	if err != nil {
		return err
	}
	current := uint64(len(versions))
	if current >= s.Epoch && sameStatement(versions[s.Epoch-1], s) {
		return nil
	}
	if current != s.Epoch-1 {
		return &EpochConflictError{Barrier: s.Barrier, Epoch: s.Epoch, Current: current}
	}
	next, err := encodeRecord(append(versions, s))
	if err != nil {
		return &StoreError{Op: "encode init record", Err: err}
	}
	stored, err := b.kv.PutIf(ctx, recordKey(s.Barrier), raw, next)
	if err != nil {
		return &StoreError{Op: "advance init record", Err: err}
	}
	if stored {
		return nil
	}
	// Lost a race: report the epoch that won, or succeed if it was this one.
	if versions, _, err = b.load(ctx, s.Barrier); err != nil {
		return err
	}
	if uint64(len(versions)) >= s.Epoch && sameStatement(versions[s.Epoch-1], s) {
		return nil
	}
	return &EpochConflictError{Barrier: s.Barrier, Epoch: s.Epoch, Current: uint64(len(versions))}
}

// Versions returns every recorded epoch of barrier's statement, epoch 1
// first; none when it was never claimed.
func (b *Book) Versions(ctx context.Context, barrier clusterseal.Barrier) ([]InitStatement, error) {
	versions, _, err := b.load(ctx, barrier)
	return versions, err
}

// load reads barrier's record: its versions and its exact stored bytes.
func (b *Book) load(ctx context.Context, barrier clusterseal.Barrier) ([]InitStatement, []byte, error) {
	key := recordKey(barrier)
	raw, found, err := b.kv.Get(ctx, key)
	if err != nil {
		return nil, nil, &StoreError{Op: "read init record", Err: err}
	}
	if !found {
		return nil, nil, nil
	}
	versions, err := decodeRecord(key, barrier, raw)
	if err != nil {
		return nil, nil, err
	}
	return versions, raw, nil
}

// sameStatement reports whether a and b state the same thing: every signed
// field, the signer's key and certificate among them. The signature is not
// compared -- an ECDSA signer signs the same statement differently every
// call -- so both must already have verified.
func sameStatement(a, b InitStatement) bool {
	// The issue time records when a statement was signed, not what it states:
	// a retry signs the same statement again, a second or more later.
	a.IssuedAt, b.IssuedAt = 0, 0
	return bytes.Equal(a.Message(), b.Message())
}
