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
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
)

func TestBook_ClaimRecordsEpochOne(t *testing.T) {
	ctx, kv, admin := context.Background(), newMemKV(), newKey(t)
	anchors := Anchors{FirstAdmin: thumbprint(t, admin.Public())}
	book := NewBook(kv, nil)
	s := signed(t, admin, unsigned(1, "kms://acme/key"))
	if err := book.Claim(ctx, s, anchors); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if _, ok, _ := kv.Get(ctx, Prefix+string(s.Barrier)); !ok {
		t.Fatalf("no record under %q", Prefix+string(s.Barrier))
	}
	got, err := book.Versions(ctx, s.Barrier)
	if err != nil || len(got) != 1 || got[0].KeyRef != "kms://acme/key" {
		t.Fatalf("Versions = %+v, %v", got, err)
	}
	// A retry of the same statement is the same claim.
	if err := book.Claim(ctx, s, anchors); err != nil {
		t.Fatalf("idempotent Claim: %v", err)
	}
}

// TestBook_ClaimAcceptsTheSameStatementSignedAgain asserts a retry is judged
// by what the owner stated, not by its signature bytes: an ECDSA signer signs
// the same statement differently every call, and a claim whose local barrier
// init failed must still be completable by the owner's next attempt.
func TestBook_ClaimAcceptsTheSameStatementSignedAgain(t *testing.T) {
	ctx, admin := context.Background(), newKey(t)
	anchors := Anchors{FirstAdmin: thumbprint(t, admin.Public())}
	book := NewBook(newMemKV(), nil)
	first := signed(t, admin, unsigned(1, "kms://acme/key"))
	if err := book.Claim(ctx, first, anchors); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	// A retry signs again a second later: the issue time records when, not
	// what, and does not make it another statement.
	retry := unsigned(1, "kms://acme/key")
	retry.IssuedAt++
	again := signed(t, admin, retry)
	if bytes.Equal(again.Signature, first.Signature) {
		t.Fatal("the signer repeated a signature; the retry proves nothing")
	}
	if err := book.Claim(ctx, again, anchors); err != nil {
		t.Fatalf("Claim of the same statement signed again = %v, want nil", err)
	}
	var already *BarrierAlreadyInitializedError
	if err := book.Claim(ctx, signed(t, admin, unsigned(1, "kms://acme/other")), anchors); !errors.As(err, &already) {
		t.Fatalf("Claim of another statement = %v, want *BarrierAlreadyInitializedError", err)
	}
}

// The red test of PN-2: of two concurrent claims on one barrier exactly one
// wins, and the loser is told the barrier is already initialised.
func TestBook_ConcurrentClaimsOneWins(t *testing.T) {
	ctx, kv, admin := context.Background(), newMemKV(), newKey(t)
	anchors := Anchors{FirstAdmin: thumbprint(t, admin.Public())}
	book := NewBook(kv, nil)
	claims := []InitStatement{
		signed(t, admin, unsigned(1, "kms://acme/a")),
		signed(t, admin, unsigned(1, "kms://acme/b")),
	}
	errs := make([]error, len(claims))
	var start, done sync.WaitGroup
	start.Add(1)
	for i, s := range claims {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()
			errs[i] = book.Claim(ctx, s, anchors)
		}()
	}
	start.Done()
	done.Wait()

	won, lost := 0, 0
	for _, err := range errs {
		var already *BarrierAlreadyInitializedError
		switch {
		case err == nil:
			won++
		case errors.As(err, &already):
			lost++
			if already.Barrier != clusterseal.TenantBarrier("acme") {
				t.Fatalf("loser names barrier %q", already.Barrier)
			}
		default:
			t.Fatalf("unexpected claim error %v (%T)", err, err)
		}
	}
	if won != 1 || lost != 1 {
		t.Fatalf("won=%d lost=%d, want exactly one winner", won, lost)
	}
	got, err := book.Versions(ctx, clusterseal.TenantBarrier("acme"))
	if err != nil || len(got) != 1 {
		t.Fatalf("Versions = %+v, %v", got, err)
	}
	winner := claims[0]
	if errs[0] != nil {
		winner = claims[1]
	}
	if got[0].KeyRef != winner.KeyRef {
		t.Fatalf("record holds %q, the winner claimed %q", got[0].KeyRef, winner.KeyRef)
	}
}

func TestBook_ClaimRefusals(t *testing.T) {
	ctx, admin, other := context.Background(), newKey(t), newKey(t)
	anchors := Anchors{FirstAdmin: thumbprint(t, admin.Public())}

	t.Run("signed by another key writes nothing", func(t *testing.T) {
		kv := newMemKV()
		err := NewBook(kv, nil).Claim(ctx, signed(t, other, unsigned(1, "")), anchors)
		wantReason(t, err, ReasonWrongSigner)
		if len(kv.m) != 0 {
			t.Fatal("a refused statement was stored")
		}
	})
	t.Run("claim must be epoch one", func(t *testing.T) {
		var conflict *EpochConflictError
		err := NewBook(newMemKV(), nil).Claim(ctx, signed(t, admin, unsigned(2, "")), anchors)
		if !errors.As(err, &conflict) || conflict.Epoch != 2 {
			t.Fatalf("Claim(epoch 2) = %v, want *EpochConflictError", err)
		}
	})
	t.Run("inactive feature", func(t *testing.T) {
		kv := newMemKV()
		var notActive *featuregate.NotActiveError
		err := NewBook(kv, notActivePerNodeBarrierKeys).Claim(ctx, signed(t, admin, unsigned(1, "")), anchors)
		if !errors.As(err, &notActive) || notActive.Feature != featuregate.PerNodeBarrierKeys {
			t.Fatalf("Claim before activation = %v, want NotActiveError(%s)", err, featuregate.PerNodeBarrierKeys)
		}
		if len(kv.m) != 0 {
			t.Fatal("a claim was stored before activation")
		}
	})
	t.Run("store failure", func(t *testing.T) {
		kv := newMemKV()
		kv.fail(errors.New("unavailable"))
		var storeErr *StoreError
		if err := NewBook(kv, nil).Claim(ctx, signed(t, admin, unsigned(1, "")), anchors); !errors.As(err, &storeErr) {
			t.Fatalf("Claim on a failing store = %v, want *StoreError", err)
		}
	})
}

func TestBook_AdvanceIsACompareAndSetOnEpoch(t *testing.T) {
	ctx, kv, admin, ca := context.Background(), newMemKV(), newKey(t), newCA(t)
	book := NewBook(kv, nil)
	if err := book.Claim(ctx, signed(t, admin, unsigned(1, "")), Anchors{FirstAdmin: thumbprint(t, admin.Public())}); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	anchors := Anchors{CA: ca.pool, At: testNow}
	next := func(epoch uint64, keyRef string) InitStatement {
		s := unsigned(epoch, keyRef)
		s.SignerCertificate = ca.issue(t, admin.Public())
		return signed(t, admin, s)
	}

	two := next(2, "kms://acme/two")
	if err := book.Advance(ctx, two, anchors); err != nil {
		t.Fatalf("Advance(2): %v", err)
	}
	if err := book.Advance(ctx, two, anchors); err != nil {
		t.Fatalf("idempotent Advance(2): %v", err)
	}
	var conflict *EpochConflictError
	if err := book.Advance(ctx, next(2, "kms://acme/rival"), anchors); !errors.As(err, &conflict) || conflict.Current != 2 {
		t.Fatalf("rival Advance(2) = %v, want *EpochConflictError at 2", err)
	}
	if err := book.Advance(ctx, next(4, ""), anchors); !errors.As(err, &conflict) || conflict.Current != 2 {
		t.Fatalf("Advance(4) skipping 3 = %v, want *EpochConflictError", err)
	}
	if err := book.Advance(ctx, signed(t, admin, unsigned(1, "")), anchors); !errors.As(err, &conflict) {
		t.Fatalf("Advance(1) = %v, want *EpochConflictError", err)
	}
	got, err := book.Versions(ctx, two.Barrier)
	if err != nil || len(got) != 2 || got[1].KeyRef != "kms://acme/two" {
		t.Fatalf("Versions = %+v, %v", got, err)
	}
}

// TestBook_AdvanceAcceptsTheSameStatementSignedAgain asserts a re-key retry
// signed afresh under the same certificate is the epoch already recorded.
func TestBook_AdvanceAcceptsTheSameStatementSignedAgain(t *testing.T) {
	ctx, admin, ca := context.Background(), newKey(t), newCA(t)
	book := NewBook(newMemKV(), nil)
	if err := book.Claim(ctx, signed(t, admin, unsigned(1, "")), Anchors{FirstAdmin: thumbprint(t, admin.Public())}); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	anchors := Anchors{CA: ca.pool, At: testNow}
	two := unsigned(2, "kms://acme/two")
	two.SignerCertificate = ca.issue(t, admin.Public())
	first, again := signed(t, admin, two), signed(t, admin, two)
	if bytes.Equal(again.Signature, first.Signature) {
		t.Fatal("the signer repeated a signature; the retry proves nothing")
	}
	if err := book.Advance(ctx, first, anchors); err != nil {
		t.Fatalf("Advance(2): %v", err)
	}
	if err := book.Advance(ctx, again, anchors); err != nil {
		t.Fatalf("Advance(2) signed again = %v, want nil", err)
	}
}

func TestBook_AdvanceRefusals(t *testing.T) {
	ctx, admin, ca := context.Background(), newKey(t), newCA(t)
	anchors := Anchors{CA: ca.pool, At: testNow}
	s := unsigned(2, "")
	s.SignerCertificate = ca.issue(t, admin.Public())
	s = signed(t, admin, s)

	t.Run("nothing claimed", func(t *testing.T) {
		var conflict *EpochConflictError
		if err := NewBook(newMemKV(), nil).Advance(ctx, s, anchors); !errors.As(err, &conflict) || conflict.Current != 0 {
			t.Fatalf("Advance on an unclaimed barrier = %v, want *EpochConflictError at 0", err)
		}
	})
	t.Run("untrusted signer", func(t *testing.T) {
		err := NewBook(newMemKV(), nil).Advance(ctx, s, Anchors{CA: newCA(t).pool, At: testNow})
		wantReason(t, err, ReasonUntrustedCertificate)
	})
	t.Run("inactive feature", func(t *testing.T) {
		var notActive *featuregate.NotActiveError
		if err := NewBook(newMemKV(), notActivePerNodeBarrierKeys).Advance(ctx, s, anchors); !errors.As(err, &notActive) {
			t.Fatalf("Advance before activation = %v, want NotActiveError", err)
		}
	})
}

// Advance's compare-and-set can lose to a write that lands after its read:
// a rival epoch is a conflict, the same statement written by a retry is not.
func TestBook_AdvanceLostRace(t *testing.T) {
	ctx, admin, ca := context.Background(), newKey(t), newCA(t)
	anchors := Anchors{CA: ca.pool, At: testNow}
	one := signed(t, admin, unsigned(1, ""))
	next := func(keyRef string) InitStatement {
		s := unsigned(2, keyRef)
		s.SignerCertificate = ca.issue(t, admin.Public())
		return signed(t, admin, s)
	}
	two, rival := next("kms://acme/two"), next("kms://acme/rival")
	race := func(t *testing.T, winner InitStatement) error {
		kv := &racingKV{memKV: newMemKV(), rival: mustEncode(t, []InitStatement{one, winner})}
		kv.set(Prefix+string(one.Barrier), mustEncode(t, []InitStatement{one}))
		return NewBook(kv, nil).Advance(ctx, two, anchors)
	}

	t.Run("rival epoch won", func(t *testing.T) {
		var conflict *EpochConflictError
		if err := race(t, rival); !errors.As(err, &conflict) || conflict.Current != 2 {
			t.Fatalf("Advance after losing to a rival = %v, want *EpochConflictError at 2", err)
		}
	})
	t.Run("same statement won", func(t *testing.T) {
		if err := race(t, two); err != nil {
			t.Fatalf("Advance after losing to its own retry = %v, want nil", err)
		}
	})
}

func TestBook_AdvanceStoreFailures(t *testing.T) {
	ctx, admin, ca := context.Background(), newKey(t), newCA(t)
	anchors := Anchors{CA: ca.pool, At: testNow}
	one := signed(t, admin, unsigned(1, ""))
	two := unsigned(2, "")
	two.SignerCertificate = ca.issue(t, admin.Public())
	two = signed(t, admin, two)
	boom := errors.New("unavailable")

	t.Run("read", func(t *testing.T) {
		kv := newMemKV()
		kv.fail(boom)
		var storeErr *StoreError
		if err := NewBook(kv, nil).Advance(ctx, two, anchors); !errors.As(err, &storeErr) || !errors.Is(err, boom) {
			t.Fatalf("Advance on an unreadable store = %v, want *StoreError wrapping %v", err, boom)
		}
	})
	t.Run("compare-and-set", func(t *testing.T) {
		kv := &racingKV{memKV: newMemKV(), putErr: boom}
		kv.set(Prefix+string(one.Barrier), mustEncode(t, []InitStatement{one}))
		var storeErr *StoreError
		if err := NewBook(kv, nil).Advance(ctx, two, anchors); !errors.As(err, &storeErr) || storeErr.Op != "advance init record" {
			t.Fatalf("Advance on a failing compare-and-set = %v, want *StoreError", err)
		}
	})
}

// racingKV lands a rival record between a reader's Get and its PutIf, or
// fails only the PutIf.
type racingKV struct {
	*memKV
	// rival, when set, is stored under the key just before the PutIf.
	rival  []byte
	putErr error
}

func (k *racingKV) PutIf(ctx context.Context, key string, expected, value []byte) (bool, error) {
	if k.putErr != nil {
		return false, k.putErr
	}
	if k.rival != nil {
		k.set(key, k.rival)
	}
	return k.memKV.PutIf(ctx, key, expected, value)
}

func TestBook_CorruptRecordsAreRefused(t *testing.T) {
	ctx, admin := context.Background(), newKey(t)
	barrier := clusterseal.TenantBarrier("acme")
	key := Prefix + string(barrier)
	other := signed(t, admin, unsigned(1, ""))
	other.Barrier = clusterseal.TenantBarrier("globex")
	gap := signed(t, admin, unsigned(2, ""))

	cases := map[string][]byte{
		"not json":         []byte("{"),
		"no versions":      []byte(`{"versions":[]}`),
		"another barrier":  mustEncode(t, []InitStatement{other}),
		"epochs not dense": mustEncode(t, []InitStatement{gap}),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			kv := newMemKV()
			kv.set(key, raw)
			var corrupt *CorruptRecordError
			if _, err := NewBook(kv, nil).Versions(ctx, barrier); !errors.As(err, &corrupt) || corrupt.Key != key {
				t.Fatalf("Versions = %v, want *CorruptRecordError for %q", err, key)
			}
		})
	}
}

func TestBook_VersionsOfAnUnclaimedBarrierIsEmpty(t *testing.T) {
	got, err := NewBook(newMemKV(), nil).Versions(context.Background(), clusterseal.TenantBarrier("acme"))
	if err != nil || len(got) != 0 {
		t.Fatalf("Versions = %+v, %v; want none", got, err)
	}
}

func TestBook_ClaimAgainstACorruptRecordIsRefused(t *testing.T) {
	ctx, kv, admin := context.Background(), newMemKV(), newKey(t)
	s := signed(t, admin, unsigned(1, ""))
	kv.set(Prefix+string(s.Barrier), []byte("{"))
	var corrupt *CorruptRecordError
	if err := NewBook(kv, nil).Claim(ctx, s, Anchors{FirstAdmin: thumbprint(t, admin.Public())}); !errors.As(err, &corrupt) {
		t.Fatalf("Claim over a corrupt record = %v, want *CorruptRecordError", err)
	}
}

// notActivePerNodeBarrierKeys is the refusal the composer's check returns
// before activation.
func notActivePerNodeBarrierKeys() error {
	return &featuregate.NotActiveError{Feature: featuregate.PerNodeBarrierKeys}
}

func mustEncode(t *testing.T, versions []InitStatement) []byte {
	t.Helper()
	raw, err := encodeRecord(versions)
	if err != nil {
		t.Fatalf("encodeRecord: %v", err)
	}
	return raw
}
