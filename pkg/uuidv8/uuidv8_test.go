// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package uuidv8

import (
	"crypto/sha256"
	"testing"

	"github.com/google/uuid"
)

func TestFromName_IsDeterministic(t *testing.T) {
	ns := uuid.NameSpaceDNS
	first := FromName(ns, "tenant/admin")
	second := FromName(ns, "tenant/admin")

	if first != second {
		t.Fatalf("FromName is not deterministic: %s != %s", first, second)
	}
}

func TestFromName_VersionAndVariant(t *testing.T) {
	got := FromName(uuid.NameSpaceDNS, "tenant/admin")

	if got.Version() != 8 {
		t.Errorf("Version() = %d, want 8", got.Version())
	}
	if got.Variant() != uuid.RFC4122 {
		t.Errorf("Variant() = %v, want RFC4122", got.Variant())
	}
}

func TestFromName_DistinctNamesDiffer(t *testing.T) {
	ns := uuid.NameSpaceDNS
	if FromName(ns, "tenant/admin") == FromName(ns, "tenant/operator") {
		t.Fatal("distinct names produced the same UUID")
	}
	// A separator collision would make "a/b"+"c" and "a"+"/bc" collide; they
	// must not, because the name is hashed verbatim.
	if FromName(ns, "a/bc") == FromName(ns, "a/b") {
		t.Fatal("distinct names produced the same UUID")
	}
}

func TestFromName_DistinctNamespacesDiffer(t *testing.T) {
	if FromName(uuid.NameSpaceDNS, "same") == FromName(uuid.NameSpaceURL, "same") {
		t.Fatal("distinct namespaces produced the same UUID")
	}
}

// TestFromName_MatchesSHA256Prefix pins the derivation so a future refactor
// cannot silently change every identifier it produces.
func TestFromName_MatchesSHA256Prefix(t *testing.T) {
	ns := uuid.NameSpaceDNS
	name := "tenant/admin"

	h := sha256.New()
	h.Write(ns[:])
	h.Write([]byte(name))
	sum := h.Sum(nil)

	want := sum[:16]
	got := FromName(ns, name)

	for i := range want {
		expected := want[i]
		switch i {
		case 6:
			expected = (expected & 0x0f) | 0x80
		case 8:
			expected = (expected & 0x3f) | 0x80
		}
		if got[i] != expected {
			t.Fatalf("byte %d = %#x, want %#x", i, got[i], expected)
		}
	}
}

// TestFromName_EmptyName is the boundary case: an empty name is still a valid
// input and must produce a stable, well-formed UUID.
func TestFromName_EmptyName(t *testing.T) {
	got := FromName(uuid.NameSpaceDNS, "")
	if got == uuid.Nil {
		t.Fatal("empty name produced the nil UUID")
	}
	if got.Version() != 8 {
		t.Errorf("Version() = %d, want 8", got.Version())
	}
	if got != FromName(uuid.NameSpaceDNS, "") {
		t.Fatal("empty name is not deterministic")
	}
}
