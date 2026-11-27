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

package pebble

import (
	"errors"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
)

// TestFormatMajorVersionPinnedAboveRemoteLSM guards the pin against falling
// below the version the remote LSM hook raises stores to.
func TestFormatMajorVersionPinnedAboveRemoteLSM(t *testing.T) {
	if FormatMajorVersion < pebble.FormatMinForSharedObjects {
		t.Fatalf("pin %s is below FormatMinForSharedObjects %s", FormatMajorVersion, pebble.FormatMinForSharedObjects)
	}
}

// createPlain creates a store in dir on fs with plain pebble.Open at v, so
// the test controls the on-disk format Open then finds.
func createPlain(t *testing.T, fs vfs.FS, dir string, v pebble.FormatMajorVersion) {
	t.Helper()
	db, err := pebble.Open(dir, &pebble.Options{FS: fs, FormatMajorVersion: v, Logger: NewLogger(nil)})
	if err != nil {
		t.Fatalf("plain pebble.Open at %s: %v", v, err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close plain store: %v", err)
	}
}

// openAndClose opens dir on fs through Open and returns its format major
// version, or Open's error.
func openAndClose(t *testing.T, fs vfs.FS, dir string, v pebble.FormatMajorVersion) (pebble.FormatMajorVersion, error) {
	t.Helper()
	db, seals, err := Open(dir, &pebble.Options{FS: fs, FormatMajorVersion: v}, nil)
	if err != nil {
		return 0, err
	}
	got := db.FormatMajorVersion()
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	seals.Close()
	return got, nil
}

func TestOpenPinsFormatMajorVersion(t *testing.T) {
	cases := []struct {
		name string
		// setup prepares dir on fs before the Open under test.
		setup func(t *testing.T, fs vfs.FS, dir string)
		// opts is the caller's FormatMajorVersion.
		opts pebble.FormatMajorVersion
	}{
		{
			name:  "new store is created at the pin",
			setup: func(*testing.T, vfs.FS, string) {},
		},
		{
			name: "reopen keeps the pin",
			setup: func(t *testing.T, fs vfs.FS, dir string) {
				if _, err := openAndClose(t, fs, dir, 0); err != nil {
					t.Fatalf("first Open: %v", err)
				}
			},
		},
		{
			name: "older store is raised to the pin",
			setup: func(t *testing.T, fs vfs.FS, dir string) {
				createPlain(t, fs, dir, pebble.FormatFlushableIngest)
			},
		},
		{
			name:  "caller version below the pin is raised",
			setup: func(*testing.T, vfs.FS, string) {},
			opts:  pebble.FormatFlushableIngest,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := vfs.NewMem()
			tc.setup(t, fs, "db")
			got, err := openAndClose(t, fs, "db", tc.opts)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if got != FormatMajorVersion {
				t.Fatalf("format = %s, want pin %s", got, FormatMajorVersion)
			}
		})
	}
}

func TestOpenRefusesCallerVersionAbovePin(t *testing.T) {
	lowerPin(t)
	_, err := openAndClose(t, vfs.NewMem(), "db", FormatMajorVersion)
	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("err = %v, want *ConfigError", err)
	}
	if cfgErr.Field != "FormatMajorVersion" {
		t.Fatalf("Field = %q, want FormatMajorVersion", cfgErr.Field)
	}
}

func TestOpenRefusesNewerOnDiskFormat(t *testing.T) {
	fs := vfs.NewMem()
	createPlain(t, fs, "db", FormatMajorVersion)
	lowerPin(t)

	_, err := openAndClose(t, fs, "db", 0)
	var fvErr *FormatVersionError
	if !errors.As(err, &fvErr) {
		t.Fatalf("err = %v, want *FormatVersionError", err)
	}
	if fvErr.Dir != "db" || fvErr.OnDisk != FormatMajorVersion || fvErr.Pinned != formatPin {
		t.Fatalf("FormatVersionError = %+v", fvErr)
	}

	// The refusal closed the store and released its lease, so a build that
	// pins the newer format opens the same dir.
	formatPin = FormatMajorVersion
	got, err := openAndClose(t, fs, "db", 0)
	if err != nil {
		t.Fatalf("Open after refusal: %v", err)
	}
	if got != FormatMajorVersion {
		t.Fatalf("format = %s, want %s", got, FormatMajorVersion)
	}
}

// lowerPin lowers the enforced pin one version below FormatMajorVersion for
// the test, so FormatMajorVersion stands in for a newer build's format.
func lowerPin(t *testing.T) {
	t.Helper()
	formatPin = FormatMajorVersion - 1
	t.Cleanup(func() { formatPin = FormatMajorVersion })
}
