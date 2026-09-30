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

package statemachine

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
)

// TestResolveTypedWithStorage_UnderTheSealBarrier pins what a storage-generic
// state machine may compose while the seal barrier is enabled: nothing that
// would persist plaintext. Refused at resolution — before any storage is
// opened — rather than discovered on disk.
func TestResolveTypedWithStorage_UnderTheSealBarrier(t *testing.T) {
	sealed := atrest.New(sealtest.UnsealedBarrier(t))
	root := t.TempDir()

	for _, tc := range []struct {
		smType, kind string
		refused      bool
	}{
		{"disk", "file", true},
		{"disk", "pebble", true},
		{"disk", "", true},
		{"fast", "pebble", true},
		{"fast", "file", true},
		{"disk", "memory", false},
		{"fast", "memory", false},
		{"memory", "", false},
		{"highperf", "", false},
	} {
		_, err := ResolveTypedWithStorage(tc.smType, "", StorageConfig{Root: root, Kind: tc.kind, Encryptor: sealed})
		var unencrypted *SMStorageUnencryptedError
		if got := errors.As(err, &unencrypted); got != tc.refused {
			t.Errorf("%s over %q under the barrier: err = %v; refused=%v, want %v", tc.smType, tc.kind, err, got, tc.refused)
		}
		if tc.refused && unencrypted.Error() == "" {
			t.Error("SMStorageUnencryptedError has an empty message")
		}
	}

	// Without the barrier nothing is refused for encryption.
	for _, enc := range []*atrest.Encryptor{nil, atrest.New(nil)} {
		if _, err := ResolveTypedWithStorage("disk", "", StorageConfig{Root: root, Kind: "file", Encryptor: enc}); err != nil {
			t.Errorf("disk over file with the barrier off: %v", err)
		}
	}
}
