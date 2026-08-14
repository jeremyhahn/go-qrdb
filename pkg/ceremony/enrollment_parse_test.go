// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
//  1. GNU Affero General Public License v3.0 (AGPL-3.0)
//
//  2. Commercial License
//     Contact licensing@automatethethings.com for commercial licensing options.

package ceremony

import (
	"errors"
	"strings"
	"testing"
)

// TestParseEnrollmentSpec covers the forms an operator may type.
//
// The two-field case is the one that matters most: xkmsd has always documented
// `--so username:/path/to/csr.pem`, so it has to keep meaning exactly what it
// meant before this parser existed. Anything else silently breaks every
// deployment script that already runs.
func TestParseEnrollmentSpec(t *testing.T) {
	for _, tc := range []struct {
		name     string
		arg      string
		username string
		kind     EnrollmentKind
		path     string
	}{
		{"explicit csr", "alice:csr:/tmp/a.csr", "alice", EnrollmentKindCSR, "/tmp/a.csr"},
		{"explicit cert", "bob:cert:/tmp/b.crt", "bob", EnrollmentKindCert, "/tmp/b.crt"},
		{"explicit pubkey", "carol:pubkey:/tmp/c.pub", "carol", EnrollmentKindPubKey, "/tmp/c.pub"},

		// The legacy two-field form, and the reason the kind is optional.
		{"legacy two-field means csr", "dave:/tmp/d.csr", "dave", EnrollmentKindCSR, "/tmp/d.csr"},

		{"kind is case-insensitive", "erin:CERT:/tmp/e.crt", "erin", EnrollmentKindCert, "/tmp/e.crt"},
		{"surrounding space is trimmed", "  frank:cert:/tmp/f.crt  ", "frank", EnrollmentKindCert, "/tmp/f.crt"},

		// A path may contain colons. The kind is recognised by matching the
		// known set, not by counting separators, so a second field that is not
		// a kind is simply the start of the path.
		{"path containing colons", "grace:/etc/ssl:certs/g.csr", "grace", EnrollmentKindCSR, "/etc/ssl:certs/g.csr"},
		{"kind then path containing colons", "heidi:cert:/etc/ssl:certs/h.crt", "heidi", EnrollmentKindCert, "/etc/ssl:certs/h.crt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec, err := ParseEnrollmentSpec(tc.arg)
			if err != nil {
				t.Fatalf("ParseEnrollmentSpec(%q): %v", tc.arg, err)
			}
			if spec.Username != tc.username {
				t.Errorf("username = %q, want %q", spec.Username, tc.username)
			}
			if spec.Kind != tc.kind {
				t.Errorf("kind = %q, want %q", spec.Kind, tc.kind)
			}
			if spec.Path != tc.path {
				t.Errorf("path = %q, want %q", spec.Path, tc.path)
			}
		})
	}
}

// TestParseEnrollmentSpecBadInput covers the refusals.
func TestParseEnrollmentSpecBadInput(t *testing.T) {
	for _, tc := range []struct {
		name string
		arg  string
	}{
		{"empty", ""},
		{"whitespace only", "   "},
		{"no separator", "alice"},
		{"no username", ":csr:/tmp/a.csr"},
		{"no path after kind", "alice:csr:"},
		{"no path at all", "alice:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var invalid *InvalidEnrollmentSpecError
			if _, err := ParseEnrollmentSpec(tc.arg); !errors.As(err, &invalid) {
				t.Fatalf("ParseEnrollmentSpec(%q) err = %v, want InvalidEnrollmentSpecError", tc.arg, err)
			}
		})
	}
}

// TestInvalidEnrollmentSpecErrorListsAcceptedKinds checks the refusal tells the
// operator what they could have typed. An error that only says "invalid" leaves
// them guessing at a vocabulary they cannot see.
func TestInvalidEnrollmentSpecErrorListsAcceptedKinds(t *testing.T) {
	_, err := ParseEnrollmentSpec("alice")
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, kind := range EnrollmentKindNames() {
		if !strings.Contains(err.Error(), kind) {
			t.Errorf("error %q does not mention accepted kind %q", err.Error(), kind)
		}
	}
}

// TestParseEnrollmentSpecsRejectsDuplicateUsername pins the duplicate rule.
//
// Keeping the last entry would enroll an identity with material the operator
// did not choose, and enrolling both would give one person two shares of an
// M-of-N quorum -- which lowers the real threshold without anyone being told.
func TestParseEnrollmentSpecsRejectsDuplicateUsername(t *testing.T) {
	var duplicate *DuplicateEnrollmentUsernameError
	_, err := ParseEnrollmentSpecs([]string{"alice:csr:/tmp/a.csr", "alice:cert:/tmp/a.crt"})
	if !errors.As(err, &duplicate) {
		t.Fatalf("err = %v, want DuplicateEnrollmentUsernameError", err)
	}
}

// TestParseEnrollmentSpecsAcceptsDistinctUsernames is the counterweight: the
// duplicate check must not reject an ordinary multi-officer ceremony.
func TestParseEnrollmentSpecsAcceptsDistinctUsernames(t *testing.T) {
	specs, err := ParseEnrollmentSpecs([]string{
		"alice:csr:/tmp/a.csr",
		"bob:cert:/tmp/b.crt",
		"carol:/tmp/c.csr",
	})
	if err != nil {
		t.Fatalf("ParseEnrollmentSpecs: %v", err)
	}
	if len(specs) != 3 {
		t.Fatalf("got %d specs, want 3", len(specs))
	}
	if specs[2].Kind != EnrollmentKindCSR {
		t.Errorf("third spec kind = %q, want csr from the two-field form", specs[2].Kind)
	}
}
