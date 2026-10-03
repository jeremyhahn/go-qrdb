// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package permission

import "testing"

// TestSelfStrictScopeRoundTrips pins the label, because the label -- not the
// integer -- is what SetScope persists and ParseScope reads back. A scope
// whose String and ParseScope disagree writes rows that fail to decode.
func TestSelfStrictScopeRoundTrips(t *testing.T) {
	if got := ScopeSelfStrict.String(); got != "self-strict" {
		t.Fatalf("ScopeSelfStrict.String() = %q, want %q", got, "self-strict")
	}
	if !ScopeSelfStrict.Valid() {
		t.Fatal("ScopeSelfStrict.Valid() = false; SetScope refuses a scope that is not Valid")
	}
	got, err := ParseScope("self-strict")
	if err != nil {
		t.Fatalf("ParseScope(\"self-strict\"): %v", err)
	}
	if got != ScopeSelfStrict {
		t.Fatalf("ParseScope(\"self-strict\") = %v, want ScopeSelfStrict", got)
	}
}

// TestEscrowIsSelfStrict pins the reclassification itself. Escrow was
// ScopeSelf, which the system-tenant rule lifts across every tenant, so a
// platform admin could recover any tenant's escrowed key material.
func TestEscrowIsSelfStrict(t *testing.T) {
	for _, p := range []Permission{EscrowCreate, EscrowList, EscrowRecover, EscrowRevoke} {
		s, err := LookupScope(p)
		if err != nil {
			t.Errorf("LookupScope(%q): %v", p, err)
			continue
		}
		if s != ScopeSelfStrict {
			t.Errorf("LookupScope(%q) = %v, want ScopeSelfStrict", p, s)
		}
	}
}

// TestMVCCCompactIsSelfStrict pins compaction to the caller's own tenant for
// every identity. It was ScopeCross -- admitted only for, and to, the system
// tenant -- while a compaction discarded the whole store's history; with a
// compaction floor per database it is each tenant's act on its own history,
// and a system lift onto another tenant's would be a cross-tenant act.
func TestMVCCCompactIsSelfStrict(t *testing.T) {
	s, err := LookupScope(MVCCCompact)
	if err != nil {
		t.Fatalf("LookupScope(%q): %v", MVCCCompact, err)
	}
	if s != ScopeSelfStrict {
		t.Fatalf("LookupScope(%q) = %v, want ScopeSelfStrict", MVCCCompact, s)
	}
	if IsCrossScope(MVCCCompact) {
		t.Fatal("mvcc:compact is still cross-scope")
	}
	if !IsRegistered(MVCCCompact) {
		t.Fatal("mvcc:compact is not registered")
	}
}

// TestMVCCReads_RemainSelfScope is a regression guard: only compaction moved.
// The revision-aware read verbs stay ScopeSelf.
func TestMVCCReads_RemainSelfScope(t *testing.T) {
	for _, p := range []Permission{MVCCPutWithLease, MVCCGetRevision, MVCCGetAtRevision, MVCCRange, MVCCCurrentRevision} {
		s, err := LookupScope(p)
		if err != nil {
			t.Fatalf("LookupScope(%q): %v", p, err)
		}
		if s != ScopeSelf {
			t.Errorf("LookupScope(%q) = %v, want ScopeSelf", p, s)
		}
	}
}

// TestDatabaseBarrierIsSelfStrict pins the database barrier permissions to
// the caller's own tenant. They were ScopeSelf, which the system-tenant rule
// lifts across every tenant, so a system-tenant identity passed the
// authorizer for another tenant's database barrier: sealing it, unsealing it
// and reading its status. A database barrier is sealed by the tenant that owns
// the database and by no one else (docs/barrier-seal).
func TestDatabaseBarrierIsSelfStrict(t *testing.T) {
	for _, p := range []Permission{DatabasesSeal, DatabasesUnseal, DatabasesBarrierStatus} {
		s, err := LookupScope(p)
		if err != nil {
			t.Errorf("LookupScope(%q): %v", p, err)
			continue
		}
		if s != ScopeSelfStrict {
			t.Errorf("LookupScope(%q) = %v, want ScopeSelfStrict", p, s)
		}
	}
}

// TestSelfStrictIsDistinctFromItsNeighbours guards the one confusion this
// scope invites: that it is a synonym for one of the two it sits between.
func TestSelfStrictIsDistinctFromItsNeighbours(t *testing.T) {
	if ScopeSelfStrict == ScopeSelf || ScopeSelfStrict == ScopeTenantOnly || ScopeSelfStrict == ScopeCross {
		t.Fatal("ScopeSelfStrict collides with another scope constant")
	}
	for _, label := range []string{"self", "cross", "tenant-only"} {
		s, err := ParseScope(label)
		if err != nil {
			t.Fatalf("ParseScope(%q): %v", label, err)
		}
		if s == ScopeSelfStrict {
			t.Fatalf("ParseScope(%q) returned ScopeSelfStrict", label)
		}
	}
}
