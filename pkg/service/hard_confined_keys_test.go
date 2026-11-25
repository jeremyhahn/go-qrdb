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
	"strings"
	"testing"
)

// delegationPolicyKey is a standing delegation policy's record, as pkg/cert writes
// it for a shared tenant: confined whole, whatever its suffix.
const delegationPolicyKey = delegationPolicyConfinedPrefix + "acme/billing"

// TestHardConfinedKV_RefusesEveryVerbOnADelegationPolicy: an embedded
// caller holding kv:put cannot forge, read, delete or enumerate a
// delegation policy (go-qrdb#1187).
func TestHardConfinedKV_RefusesEveryVerbOnADelegationPolicy(t *testing.T) {
	ctx := context.Background()
	fake := newFakeKV()
	fake.store[delegationPolicyKey] = []byte(`{"principal":"billing"}`)
	kv := NewHardConfinedKV(fake)

	if err := kv.Put(ctx, delegationPolicyKey, []byte(`{"principal":"billing","scope":["*"]}`)); err == nil {
		t.Fatal("Put on a policy key succeeded")
	} else {
		assertHardConfinedError(t, err)
		if strings.Contains(err.Error(), "billing") {
			t.Fatalf("the refusal echoes the key: %v", err)
		}
	}
	if _, err := kv.Get(ctx, delegationPolicyKey); err == nil {
		t.Fatal("Get on a policy key succeeded")
	} else {
		assertHardConfinedError(t, err)
	}
	if err := kv.Delete(ctx, delegationPolicyKey); err == nil {
		t.Fatal("Delete on a policy key succeeded")
	} else {
		assertHardConfinedError(t, err)
	}
	for _, root := range []string{delegationPolicyConfinedPrefix, delegationPolicyConfinedPrefix + "acme/"} {
		if _, err := kv.List(ctx, root); err == nil {
			t.Fatalf("List(%q) succeeded", root)
		} else {
			assertHardConfinedError(t, err)
		}
		if _, err := kv.ListAll(ctx, root); err == nil {
			t.Fatalf("ListAll(%q) succeeded", root)
		} else {
			assertHardConfinedError(t, err)
		}
		if _, err := kv.Scan(ctx, root); err == nil {
			t.Fatalf("Scan(%q) succeeded", root)
		} else {
			assertHardConfinedError(t, err)
		}
	}
	if string(fake.store[delegationPolicyKey]) != `{"principal":"billing"}` {
		t.Fatalf("the policy record changed: %s", fake.store[delegationPolicyKey])
	}

	// A broader sweep does not surface it either.
	data, err := kv.Scan(ctx, InternalKeyPrefixSystem)
	if err != nil {
		t.Fatalf("Scan(%q): %v", InternalKeyPrefixSystem, err)
	}
	if _, ok := data[delegationPolicyKey]; ok {
		t.Fatal("a __sys: sweep returned the policy record")
	}
}

// TestHardConfinedTransaction_RefusesADelegationPolicy: a transaction
// cannot write or compare against a policy either.
func TestHardConfinedTransaction_RefusesADelegationPolicy(t *testing.T) {
	ctx := context.Background()
	fake := &fakeTxn{}
	txn := NewHardConfinedTransaction(fake)
	ops := []Op{{Type: OpPut, Key: []byte(delegationPolicyKey), Value: []byte("x")}}
	if _, err := txn.Execute(ctx, nil, ops, nil); err == nil {
		t.Fatal("a transaction writing a policy succeeded")
	} else {
		assertHardConfinedError(t, err)
	}
	ranged := []Op{{Type: OpDelete, Key: []byte(delegationPolicyConfinedPrefix), RangeEnd: []byte(delegationPolicyConfinedPrefix + "\xff")}}
	if _, err := txn.Execute(ctx, nil, ranged, nil); err == nil {
		t.Fatal("a transaction deleting the policy range succeeded")
	} else {
		assertHardConfinedError(t, err)
	}
	if fake.called {
		t.Fatal("a refused transaction reached the underlying servicer")
	}
}

// TestHardConfinedKeys_PolicyPrefixIsWholeAndCAPrefixesStayMaterialOnly
// pins the two rules side by side, and pkg/cert's own narrow self-scan.
func TestHardConfinedKeys_PolicyPrefixIsWholeAndCAPrefixesStayMaterialOnly(t *testing.T) {
	for key, want := range map[string]bool{
		delegationPolicyKey:                                  true,
		delegationPolicyConfinedPrefix:                       true,
		delegationPolicyConfinedPrefix + "acme/x/cert":       true,
		InternalKeyPrefixSystem + "cert_ca/acme/material":    true,
		InternalKeyPrefixSystem + "cert_ca/acme/certificate": false,
		InternalKeyPrefixSystem + "cert_delegation":          false,
	} {
		if got := isHardConfinedKey(key); got != want {
			t.Errorf("isHardConfinedKey(%q) = %v, want %v", key, got, want)
		}
	}
	if !isHardConfinedRoot(delegationPolicyConfinedPrefix) {
		t.Error("the policy prefix is not a confined root")
	}
	if !hardConfinedPrefixOverlaps(InternalKeyPrefixSystem) || !hardConfinedRangeOverlaps(delegationPolicyConfinedPrefix+"a", delegationPolicyConfinedPrefix+"b") {
		t.Error("a sweep over the policy prefix does not overlap it")
	}
	if !isHardConfinedKeyForScan(delegationPolicyKey, InternalKeyPrefixSystem) {
		t.Error("a __sys: sweep keeps the policy record")
	}
	if isHardConfinedKeyForScan(delegationPolicyKey, delegationPolicyConfinedPrefix+"acme/") {
		t.Error("pkg/cert's own narrow self-scan loses the policy record")
	}
	if got := (&HardConfinedKeyError{Key: delegationPolicyKey}).Error(); strings.Contains(got, "acme") || !strings.Contains(got, delegationPolicyConfinedPrefix) {
		t.Errorf("refusal = %q, want the namespace and not the key", got)
	}
}

// TestHardConfinedKV_RefusesEveryVerbOnTheFirstBootHandoffs: the pending
// system SO and pending admin certificate records authorize an enrolment,
// so an embedded caller holding kv:put can neither forge, read, replace
// nor delete them, and a broader sweep does not surface them (PR #1579
// review round 2, P0).
func TestHardConfinedKV_RefusesEveryVerbOnTheFirstBootHandoffs(t *testing.T) {
	for _, key := range []string{pendingSystemSOConfinedKey, pendingAdminCertConfinedKey} {
		t.Run(key, func(t *testing.T) {
			ctx := context.Background()
			fake := newFakeKV()
			fake.store[key] = []byte(`{"username":"so"}`)
			fake.store["system/user/1"] = []byte(`{}`)
			kv := NewHardConfinedKV(fake)

			if err := kv.Put(ctx, key, []byte(`{"username":"forged"}`)); err == nil {
				t.Fatal("Put on the handoff record succeeded")
			} else {
				assertHardConfinedError(t, err)
			}
			if _, err := kv.Get(ctx, key); err == nil {
				t.Fatal("Get on the handoff record succeeded")
			} else {
				assertHardConfinedError(t, err)
			}
			if err := kv.Delete(ctx, key); err == nil {
				t.Fatal("Delete on the handoff record succeeded")
			} else {
				assertHardConfinedError(t, err)
			}
			if _, err := kv.List(ctx, key); err == nil {
				t.Fatal("List rooted at the handoff record succeeded")
			} else {
				assertHardConfinedError(t, err)
			}
			if _, err := kv.ListAll(ctx, key); err == nil {
				t.Fatal("ListAll rooted at the handoff record succeeded")
			} else {
				assertHardConfinedError(t, err)
			}
			if _, err := kv.Scan(ctx, key); err == nil {
				t.Fatal("Scan rooted at the handoff record succeeded")
			} else {
				assertHardConfinedError(t, err)
			}
			if string(fake.store[key]) != `{"username":"so"}` {
				t.Fatalf("the handoff record changed: %s", fake.store[key])
			}
			for _, root := range []string{"system/", ""} {
				data, err := kv.Scan(ctx, root)
				if err != nil {
					t.Fatalf("Scan(%q): %v", root, err)
				}
				if _, ok := data[key]; ok {
					t.Fatalf("a Scan(%q) sweep returned the handoff record", root)
				}
			}
		})
	}
}

// TestHardConfinedTransaction_RefusesTheFirstBootHandoffs: neither a
// transaction write of a handoff record nor a ranged delete over "system/"
// reaches the underlying servicer.
func TestHardConfinedTransaction_RefusesTheFirstBootHandoffs(t *testing.T) {
	ctx := context.Background()
	for _, key := range []string{pendingSystemSOConfinedKey, pendingAdminCertConfinedKey} {
		fake := &fakeTxn{}
		txn := NewHardConfinedTransaction(fake)
		ops := []Op{{Type: OpPut, Key: []byte(key), Value: []byte("x")}}
		if _, err := txn.Execute(ctx, nil, ops, nil); err == nil {
			t.Fatalf("a transaction writing %q succeeded", key)
		} else {
			assertHardConfinedError(t, err)
		}
		if fake.called {
			t.Fatal("a refused transaction reached the underlying servicer")
		}
	}
	fake := &fakeTxn{}
	txn := NewHardConfinedTransaction(fake)
	ranged := []Op{{Type: OpDelete, Key: []byte("system/"), RangeEnd: []byte("system0")}}
	if _, err := txn.Execute(ctx, nil, ranged, nil); err == nil {
		t.Fatal("a transaction deleting the system/ range succeeded")
	} else {
		assertHardConfinedError(t, err)
	}
	if fake.called {
		t.Fatal("a refused transaction reached the underlying servicer")
	}
}

// TestHardConfinedKeys_FirstBootHandoffsAreExactKeys: only the two
// handoff records are confined in "system/"; every other identity key
// stays reachable.
func TestHardConfinedKeys_FirstBootHandoffsAreExactKeys(t *testing.T) {
	for key, want := range map[string]bool{
		pendingSystemSOConfinedKey:  true,
		pendingAdminCertConfinedKey: true,
		"system/user/1":             false,
		"system/pending":            false,
		"system/":                   false,
	} {
		if got := isHardConfinedKey(key); got != want {
			t.Errorf("isHardConfinedKey(%q) = %v, want %v", key, got, want)
		}
	}
	if !hardConfinedPrefixOverlaps("system/") {
		t.Error("a system/ sweep does not overlap the handoff records")
	}
	if !isHardConfinedRoot(pendingSystemSOConfinedKey) || !isHardConfinedRoot(pendingAdminCertConfinedKey) {
		t.Error("a handoff record is not a confined root")
	}
	if hardConfinedRangeOverlaps("system/user/", "system/user0") {
		t.Error("a range over system/user/ is treated as reaching a handoff record")
	}
}
