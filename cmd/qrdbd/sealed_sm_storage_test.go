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

package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// TestBuildSMCreateFunc_RefusesPlaintextSMStorageUnderTheBarrier is the
// daemon's start-time refusal: with the seal barrier enabled, a
// storage-generic --sm over storage that cannot be encrypted (file) is refused
// with SealedSMStoragePlaintextError; pebble storage, which the barrier
// encrypts, and memory storage, which persists nothing, still build.
func TestBuildSMCreateFunc_RefusesPlaintextSMStorageUnderTheBarrier(t *testing.T) {
	sealed := atrest.New(sealtest.UnsealedBarrier(t))
	lr, hub := lease.NewLessor(), watch.NewHub()
	t.Cleanup(hub.Close)

	for _, tc := range []struct {
		smType, storage string
		refused         bool
	}{
		{"disk", "file", true},
		{"fast", "file", true},
		{"disk", "pebble", false},
		{"fast", "", false},
		{"disk", "memory", false},
		{"memory", "", false},
		{"pebble", "", false},
		{"mvcc", "", false},
	} {
		f := &startFlags{smType: tc.smType, smStorage: tc.storage, dataDir: t.TempDir()}
		fn, err := buildSMCreateFunc(f, sealed, lr, hub, nil)
		var plaintext *SealedSMStoragePlaintextError
		if got := errors.As(err, &plaintext); got != tc.refused {
			t.Fatalf("--sm %s --sm-storage %q under the barrier: err = %v; refused=%v, want %v",
				tc.smType, tc.storage, err, got, tc.refused)
		}
		if tc.refused {
			if !strings.Contains(plaintext.Error(), "plaintext") || plaintext.Unwrap() == nil {
				t.Errorf("refusal %q must name the plaintext and wrap the registry's error", plaintext.Error())
			}
			continue
		}
		if fn == nil {
			t.Errorf("--sm %s --sm-storage %q built no factory", tc.smType, tc.storage)
		}
	}

	// The barrier off: storage-backed disk builds, as it always has.
	f := &startFlags{smType: "disk", smStorage: "file", dataDir: t.TempDir()}
	if _, err := buildSMCreateFunc(f, atrest.New(nil), lr, hub, nil); err != nil {
		t.Fatalf("--sm disk --sm-storage file with the barrier off: %v", err)
	}
}
