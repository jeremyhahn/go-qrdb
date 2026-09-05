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

package auth

import (
	"errors"
	"strings"
	"testing"
)

func TestAttestationLevel_Valid(t *testing.T) {
	for _, tc := range []struct {
		level AttestationLevel
		want  bool
	}{
		{AttestationLevelNone, true},
		{AttestationLevelSoftware, true},
		{AttestationLevelHardware, true},
		{"", false},         // unset is an unanswered question, not a level
		{"HARDWARE", false}, // the vocabulary is exact; policy compares bytes
		{"hardwre", false},
		{"attested", false},
	} {
		if got := tc.level.Valid(); got != tc.want {
			t.Errorf("AttestationLevel(%q).Valid() = %v, want %v", tc.level, got, tc.want)
		}
	}
	if AttestationLevelHardware.String() != "hardware" {
		t.Errorf("String() = %q", AttestationLevelHardware.String())
	}
}

// TestMetadataKeys_AreInsideTheReservedNamespace pins the property the whole
// design rests on: the keys the server writes are exactly the keys a caller
// cannot. If either key ever moved outside the prefix, a caller could set it
// and Stamp would silently overwrite -- or, worse, not.
func TestMetadataKeys_AreInsideTheReservedNamespace(t *testing.T) {
	for _, key := range []string{MetadataAttestLevel, MetadataAttestAAGUID} {
		if !strings.HasPrefix(key, ReservedMetadataPrefix) {
			t.Errorf("%q is not under the reserved prefix %q", key, ReservedMetadataPrefix)
		}
		if err := ValidateMetadata(map[string]string{key: "x"}); err == nil {
			t.Errorf("a caller-supplied %q must be refused", key)
		}
	}
	if MetadataAttestLevel != "attest_level" || MetadataAttestAAGUID != "attest_aaguid" {
		t.Errorf("key spellings moved: %q %q; consumers read these names", MetadataAttestLevel, MetadataAttestAAGUID)
	}
}

func TestVerifiedAttestation_Validate(t *testing.T) {
	if err := (VerifiedAttestation{Level: AttestationLevelHardware, AAGUID: "abcd"}).Validate(); err != nil {
		t.Fatalf("a defined level must validate: %v", err)
	}
	if err := Unattested().Validate(); err != nil {
		t.Fatalf("Unattested must validate: %v", err)
	}

	for _, level := range []AttestationLevel{"", "hardwre", "Hardware"} {
		err := (VerifiedAttestation{Level: level}).Validate()
		if err == nil {
			t.Fatalf("level %q validated", level)
		}
		var invalid *InvalidAttestationError
		if !errors.As(err, &invalid) {
			t.Fatalf("error = %T, want *InvalidAttestationError", err)
		}
		if !errors.Is(err, ErrInvalidAttestation) {
			t.Error("errors.Is(err, ErrInvalidAttestation) is false")
		}
		if invalid.Level != level {
			t.Errorf("error names level %q, want %q", invalid.Level, level)
		}
		if !strings.Contains(err.Error(), string(AttestationLevelHardware)) {
			t.Errorf("message %q does not list the defined levels", err.Error())
		}
	}
}

func TestUnattested(t *testing.T) {
	u := Unattested()
	if u.Level != AttestationLevelNone {
		t.Errorf("Level = %q, want none", u.Level)
	}
	if u.AAGUID != "" {
		t.Errorf("AAGUID = %q, want empty", u.AAGUID)
	}
}

func TestVerifiedAttestation_Stamp(t *testing.T) {
	t.Run("nil map is allocated", func(t *testing.T) {
		md := Unattested().Stamp(nil)
		if md == nil {
			t.Fatal("Stamp(nil) returned nil")
		}
		if md[MetadataAttestLevel] != string(AttestationLevelNone) {
			t.Errorf("level = %q, want none", md[MetadataAttestLevel])
		}
		if _, ok := md[MetadataAttestAAGUID]; ok {
			t.Error("an unattested factor must carry no aaguid key, not an empty one")
		}
	})

	t.Run("ordinary keys survive and the model is recorded", func(t *testing.T) {
		md := map[string]string{"vendor": "yubico"}
		got := (VerifiedAttestation{Level: AttestationLevelHardware, AAGUID: "ee882879"}).Stamp(md)
		if got["vendor"] != "yubico" {
			t.Errorf("caller metadata lost: %v", got)
		}
		if got[MetadataAttestLevel] != "hardware" || got[MetadataAttestAAGUID] != "ee882879" {
			t.Errorf("attestation not recorded: %v", got)
		}
		if len(got) != 3 {
			t.Errorf("len = %d, want 3: %v", len(got), got)
		}
	})

	t.Run("stamped output is itself inside the refused namespace", func(t *testing.T) {
		// A stamped map fed back in as caller input must be refused: that
		// is what makes "only the server writes these" a closed loop.
		md := (VerifiedAttestation{Level: AttestationLevelSoftware}).Stamp(nil)
		if err := ValidateMetadata(md); err == nil {
			t.Fatal("a stamped map re-supplied by a caller was accepted")
		}
	})
}
