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
	"context"
	"encoding/json"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
)

// AnchorPrefix is where each barrier's epoch-1 anchor is pinned in the system
// store: AnchorPrefix + barrier. It is not under Prefix, so a Watcher's scan
// of the init records never reads it as one.
const AnchorPrefix = "__sys:cluster/barrier/anchor/"

// pin is the stored anchor record.
type pin struct {
	// FirstAdmin is the SPKI thumbprint of the first admin the barrier's owner
	// was onboarded with.
	FirstAdmin string `json:"first_admin"`
}

func anchorKey(b clusterseal.Barrier) string { return AnchorPrefix + string(b) }

// Pins records the first admin each barrier's epoch-1 statement must be
// signed by. The pending first admin's record is consumed when that admin's
// account is created, but every node that initialises its key later -- a
// node that was down, a joiner -- still verifies epoch 1 against it, so the
// thumbprint is pinned, once, when the owner is onboarded.
type Pins struct {
	kv KV
}

// NewPins returns the Pins over kv, the system store.
func NewPins(kv KV) *Pins {
	return &Pins{kv: kv}
}

// PinFirstAdmin records thumbprint as b's epoch-1 signer with a guarded
// create-if-absent. Pinning the recorded thumbprint again succeeds; any other
// is refused with *AnchorConflictError: the anchor is written once.
func (p *Pins) PinFirstAdmin(ctx context.Context, b clusterseal.Barrier, thumbprint string) error {
	if b.Kind() == clusterseal.KindInvalid || thumbprint == "" {
		return &InitStatementError{Barrier: b, Epoch: 1, Reason: ReasonNoAnchor}
	}
	raw, err := json.Marshal(pin{FirstAdmin: thumbprint})
	if err != nil {
		return &StoreError{Op: "encode anchor", Err: err}
	}
	stored, err := p.kv.PutIf(ctx, anchorKey(b), nil, raw)
	if err != nil {
		return &StoreError{Op: "pin anchor", Err: err}
	}
	if stored {
		return nil
	}
	current, err := p.FirstAdmin(ctx, b)
	if err != nil {
		return err
	}
	if current == thumbprint {
		return nil
	}
	return &AnchorConflictError{Barrier: b}
}

// FirstAdmin returns the thumbprint pinned for b; empty when none is.
func (p *Pins) FirstAdmin(ctx context.Context, b clusterseal.Barrier) (string, error) {
	key := anchorKey(b)
	raw, found, err := p.kv.Get(ctx, key)
	if err != nil {
		return "", &StoreError{Op: "read anchor", Err: err}
	}
	if !found {
		return "", nil
	}
	var rec pin
	if err := json.Unmarshal(raw, &rec); err != nil {
		return "", &CorruptRecordError{Key: key, Err: err}
	}
	if rec.FirstAdmin == "" {
		return "", &CorruptRecordError{Key: key, Reason: "anchor names no first admin"}
	}
	return rec.FirstAdmin, nil
}

// Anchors is the AnchorSource over the pins: b's pinned first admin. No owner
// CA is pinned yet, so a later epoch, which only a certificate chaining to it
// may sign, has no anchor and is refused with ReasonNoAnchor.
func (p *Pins) Anchors(ctx context.Context, b clusterseal.Barrier) (Anchors, error) {
	firstAdmin, err := p.FirstAdmin(ctx, b)
	if err != nil {
		return Anchors{}, err
	}
	return Anchors{FirstAdmin: firstAdmin}, nil
}
