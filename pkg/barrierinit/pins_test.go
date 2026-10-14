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
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
)

func TestPins_FirstAdminIsWrittenOnce(t *testing.T) {
	ctx, kv := context.Background(), newMemKV()
	p := NewPins(kv)
	b := clusterseal.TenantBarrier("acme")

	if got, err := p.FirstAdmin(ctx, b); err != nil || got != "" {
		t.Fatalf("FirstAdmin before pinning = %q, %v; want none", got, err)
	}
	if err := p.PinFirstAdmin(ctx, b, "tp-1"); err != nil {
		t.Fatalf("PinFirstAdmin: %v", err)
	}
	// A retry of the same onboarding succeeds.
	if err := p.PinFirstAdmin(ctx, b, "tp-1"); err != nil {
		t.Fatalf("PinFirstAdmin retry: %v", err)
	}
	var conflict *AnchorConflictError
	if err := p.PinFirstAdmin(ctx, b, "tp-2"); !errors.As(err, &conflict) || conflict.Barrier != b {
		t.Fatalf("PinFirstAdmin(other key) = %v, want *AnchorConflictError", err)
	}
	if got, err := p.FirstAdmin(ctx, b); err != nil || got != "tp-1" {
		t.Fatalf("FirstAdmin = %q, %v; want the first pin", got, err)
	}
}

// The anchor lives outside the init records, so a watcher never reads it as
// one.
func TestPins_AnchorIsNotAnInitRecord(t *testing.T) {
	ctx, kv := context.Background(), newMemKV()
	if err := NewPins(kv).PinFirstAdmin(ctx, clusterseal.TenantBarrier("acme"), "tp"); err != nil {
		t.Fatalf("PinFirstAdmin: %v", err)
	}
	if strings.HasPrefix(AnchorPrefix, Prefix) {
		t.Fatalf("AnchorPrefix %q is under Prefix %q", AnchorPrefix, Prefix)
	}
	got, err := kv.Scan(ctx, Prefix)
	if err != nil || len(got) != 0 {
		t.Fatalf("Scan(Prefix) = %v, %v; want nothing", got, err)
	}
}

func TestPins_RefusesAnEmptyAnchor(t *testing.T) {
	ctx, p := context.Background(), NewPins(newMemKV())
	wantReason(t, p.PinFirstAdmin(ctx, clusterseal.TenantBarrier("acme"), ""), ReasonNoAnchor)
	wantReason(t, p.PinFirstAdmin(ctx, clusterseal.Barrier("bogus"), "tp"), ReasonNoAnchor)
}

func TestPins_StoreFailuresAreStoreErrors(t *testing.T) {
	ctx, kv := context.Background(), newMemKV()
	p := NewPins(kv)
	kv.fail(errors.New("down"))
	var se *StoreError
	if err := p.PinFirstAdmin(ctx, clusterseal.TenantBarrier("acme"), "tp"); !errors.As(err, &se) {
		t.Fatalf("PinFirstAdmin = %v, want *StoreError", err)
	}
	if _, err := p.FirstAdmin(ctx, clusterseal.TenantBarrier("acme")); !errors.As(err, &se) {
		t.Fatalf("FirstAdmin = %v, want *StoreError", err)
	}
}

func TestPins_CorruptAnchor(t *testing.T) {
	ctx, kv := context.Background(), newMemKV()
	p := NewPins(kv)
	b := clusterseal.TenantBarrier("acme")
	for name, raw := range map[string]string{"undecodable": "{", "empty": `{"first_admin":""}`} {
		kv.set(AnchorPrefix+string(b), []byte(raw))
		var corrupt *CorruptRecordError
		if _, err := p.FirstAdmin(ctx, b); !errors.As(err, &corrupt) {
			t.Fatalf("%s: FirstAdmin = %v, want *CorruptRecordError", name, err)
		}
		// A pin that lost to a corrupt record reports the corruption.
		if err := p.PinFirstAdmin(ctx, b, "tp"); !errors.As(err, &corrupt) {
			t.Fatalf("%s: PinFirstAdmin = %v, want *CorruptRecordError", name, err)
		}
	}
}

// A Claimer over Pins.Anchors verifies epoch 1 against the pinned first admin,
// and refuses every statement for a barrier nothing was pinned for.
func TestPins_AnchorsAreThePinnedFirstAdmin(t *testing.T) {
	ctx, kv := context.Background(), newMemKV()
	p := NewPins(kv)
	admin := newKey(t)
	raw, err := signed(t, admin, unsigned(1, "")).Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	c := NewClaimer(NewBook(kv, nil), p.Anchors)

	wantReason(t, c.ClaimTenantInit(ctx, "acme", "passphrase", "", raw), ReasonNoAnchor)
	if err := p.PinFirstAdmin(ctx, clusterseal.TenantBarrier("acme"), thumbprint(t, admin.Public())); err != nil {
		t.Fatalf("PinFirstAdmin: %v", err)
	}
	if err := c.ClaimTenantInit(ctx, "acme", "passphrase", "", raw); err != nil {
		t.Fatalf("ClaimTenantInit after the pin: %v", err)
	}
	a, err := p.Anchors(ctx, clusterseal.TenantBarrier("acme"))
	if err != nil || a.CA != nil || a.FirstAdmin != thumbprint(t, admin.Public()) {
		t.Fatalf("Anchors = %+v, %v; want the pinned first admin and no CA", a, err)
	}
}

func TestPins_AnchorsReportAStoreFailure(t *testing.T) {
	kv := newMemKV()
	kv.fail(errors.New("down"))
	var se *StoreError
	if _, err := NewPins(kv).Anchors(context.Background(), clusterseal.TenantBarrier("acme")); !errors.As(err, &se) {
		t.Fatalf("Anchors = %v, want *StoreError", err)
	}
}

func TestStatement_MarshalParseRoundTrip(t *testing.T) {
	s := signed(t, newKey(t), unsigned(1, "kms://key"))
	raw, err := s.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !sameStatement(got, s) || !bytes.Equal(got.Signature, s.Signature) {
		t.Fatalf("Parse(Marshal(s)) is not s")
	}
}

func TestStatement_ParseRefusesMalformed(t *testing.T) {
	for name, raw := range map[string][]byte{
		"empty":       nil,
		"undecodable": []byte("{"),
		"oversized":   make([]byte, MaxEncodedLength+1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse(raw)
			wantReason(t, err, ReasonMalformed)
		})
	}
}

func TestStatement_MatchesTheRequest(t *testing.T) {
	s := unsigned(1, "kms://key")
	want := Expect{Barrier: s.Barrier, Epoch: 1, StrategyID: s.StrategyID, KeyRef: "kms://key"}
	if err := s.Matches(want); err != nil {
		t.Fatalf("Matches: %v", err)
	}
	for name, mutate := range map[string]func(*Expect){
		"barrier":  func(e *Expect) { e.Barrier = clusterseal.TenantBarrier("globex") },
		"epoch":    func(e *Expect) { e.Epoch = 2 },
		"strategy": func(e *Expect) { e.StrategyID = "none" },
		"key ref":  func(e *Expect) { e.KeyRef = "kms://other" },
	} {
		e := want
		mutate(&e)
		err := s.Matches(e)
		var ise *InitStatementError
		if !errors.As(err, &ise) || ise.Reason != ReasonMismatch {
			t.Fatalf("%s: Matches = %v, want ReasonMismatch", name, err)
		}
	}
}
