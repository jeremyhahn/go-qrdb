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

package clusterkeys

import (
	"bytes"
	"testing"

	quicraftseal "github.com/jeremyhahn/go-quicraft/pkg/seal"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// TestKeyPayload_RoundTripPreservesHKDFVersion pins the wire codec against
// the follower-DEK-divergence regression: the payload once omitted
// HKDFVersion, so a v2-derived leader's key imported as version 0 (legacy
// v1) and every cross-node decrypt of leader-written ciphertext failed with
// "quicraft/seal: decryption failed" despite identical root key, salt, and
// epoch. Every ImportedKey field that feeds DEK derivation must survive the
// round trip.
func TestKeyPayload_RoundTripPreservesHKDFVersion(t *testing.T) {
	in := &seal.ImportedKey{
		RootKey:     bytes.Repeat([]byte{0xAB}, 32),
		Salt:        bytes.Repeat([]byte{0xCD}, 32),
		Epoch:       7,
		KnownEpochs: []uint64{1, 3, 7},
		HKDFVersion: quicraftseal.HKDFV2,
	}
	wire, err := marshalKeyPayload(in)
	if err != nil {
		t.Fatalf("marshalKeyPayload: %v", err)
	}
	out, err := unmarshalKeyPayload(wire)
	if err != nil {
		t.Fatalf("unmarshalKeyPayload: %v", err)
	}
	if !bytes.Equal(out.RootKey, in.RootKey) {
		t.Fatal("RootKey did not round-trip")
	}
	if !bytes.Equal(out.Salt, in.Salt) {
		t.Fatal("Salt did not round-trip")
	}
	if out.Epoch != in.Epoch {
		t.Fatalf("Epoch = %d, want %d", out.Epoch, in.Epoch)
	}
	if len(out.KnownEpochs) != len(in.KnownEpochs) {
		t.Fatalf("KnownEpochs = %v, want %v", out.KnownEpochs, in.KnownEpochs)
	}
	if out.HKDFVersion != in.HKDFVersion {
		t.Fatalf("HKDFVersion = %d, want %d — a dropped HKDF version makes "+
			"followers derive legacy-v1 DEKs and fail every cross-node decrypt",
			out.HKDFVersion, in.HKDFVersion)
	}
}

// TestKeyPayload_LegacyWireWithoutHKDFVersion pins backward compatibility:
// a payload from an older leader that predates the hkdf_version field must
// decode with version 0, which ImportRootKey documents as the legacy v1
// layout — matching what such a leader actually derives with.
func TestKeyPayload_LegacyWireWithoutHKDFVersion(t *testing.T) {
	legacy := []byte(`{"root_key":"qw==","salt":"zQ==","epoch":1,"known_epochs":[1]}`)
	out, err := unmarshalKeyPayload(legacy)
	if err != nil {
		t.Fatalf("unmarshalKeyPayload(legacy): %v", err)
	}
	if out.HKDFVersion != 0 {
		t.Fatalf("HKDFVersion = %d, want 0 for a legacy payload", out.HKDFVersion)
	}
	if out.Epoch != 1 {
		t.Fatalf("Epoch = %d, want 1", out.Epoch)
	}
}
