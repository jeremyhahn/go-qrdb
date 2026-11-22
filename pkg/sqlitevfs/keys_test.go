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

package sqlitevfs

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestDatabaseNameRefusals(t *testing.T) {
	tests := []struct {
		name string
		db   string
	}{
		{"empty", ""},
		{"too long", strings.Repeat("a", MaxDatabaseNameLen+1)},
		{"slash", "a/b"},
		{"leading slash", "/a"},
		{"NUL", "a\x00b"},
		{"control byte", "a\x1fb"},
		{"newline", "a\nb"},
		{"dot", "."},
		{"dot dot", ".."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := MetaKey(tc.db); !errors.Is(err, ErrInvalidDatabaseName) {
				t.Fatalf("MetaKey(%q) err = %v, want ErrInvalidDatabaseName", tc.db, err)
			}
			if _, err := PagePrefix(tc.db); !errors.Is(err, ErrInvalidDatabaseName) {
				t.Fatalf("PagePrefix(%q) err = %v, want ErrInvalidDatabaseName", tc.db, err)
			}
			if _, err := PageKey(tc.db, 1); !errors.Is(err, ErrInvalidDatabaseName) {
				t.Fatalf("PageKey(%q) err = %v, want ErrInvalidDatabaseName", tc.db, err)
			}
			if _, err := ParsePageKey(tc.db, []byte("sqlite/x/page/\x00\x00\x00\x01")); !errors.Is(err, ErrInvalidDatabaseName) {
				t.Fatalf("ParsePageKey(%q) err = %v, want ErrInvalidDatabaseName", tc.db, err)
			}
			if _, err := NewStore(&failingBackend{}, tc.db); !errors.Is(err, ErrInvalidDatabaseName) {
				t.Fatalf("NewStore(%q) err = %v, want ErrInvalidDatabaseName", tc.db, err)
			}
		})
	}
}

func TestDatabaseNameAccepted(t *testing.T) {
	for _, db := range []string{"app", "app.db", "...", "a b", strings.Repeat("z", MaxDatabaseNameLen), "über"} {
		if err := ValidateDatabaseName(db); err != nil {
			t.Fatalf("ValidateDatabaseName(%q) = %v, want nil", db, err)
		}
	}
}

func TestKeyLayout(t *testing.T) {
	meta, err := MetaKey("app.db")
	if err != nil {
		t.Fatal(err)
	}
	if string(meta) != "sqlite/app.db/meta" {
		t.Fatalf("MetaKey = %q", meta)
	}
	prefix, err := PagePrefix("app.db")
	if err != nil {
		t.Fatal(err)
	}
	if string(prefix) != "sqlite/app.db/page/" {
		t.Fatalf("PagePrefix = %q", prefix)
	}
	key, err := PageKey("app.db", 0x01020304)
	if err != nil {
		t.Fatal(err)
	}
	if want := "sqlite/app.db/page/\x01\x02\x03\x04"; string(key) != want {
		t.Fatalf("PageKey = %q, want %q", key, want)
	}
}

func TestPageKeyOrder(t *testing.T) {
	pgnos := []uint32{1, 2, 255, 256, 65535, 65536, 1 << 24, 1<<32 - 1}
	var prev []byte
	for _, pgno := range pgnos {
		key, err := PageKey("db", pgno)
		if err != nil {
			t.Fatal(err)
		}
		if prev != nil && bytes.Compare(prev, key) >= 0 {
			t.Fatalf("page key of %d does not sort after its predecessor", pgno)
		}
		prev = key
	}
}

func TestParsePageKeyRoundTrip(t *testing.T) {
	for _, pgno := range []uint32{1, 2, 256, 65536, 1<<32 - 1} {
		key, err := PageKey("db", pgno)
		if err != nil {
			t.Fatal(err)
		}
		got, err := ParsePageKey("db", key)
		if err != nil {
			t.Fatalf("ParsePageKey(%d): %v", pgno, err)
		}
		if got != pgno {
			t.Fatalf("ParsePageKey = %d, want %d", got, pgno)
		}
	}
}

func TestPageNumberRefusals(t *testing.T) {
	if _, err := PageKey("db", 0); !errors.Is(err, ErrInvalidPageNumber) {
		t.Fatalf("PageKey(0) err = %v, want ErrInvalidPageNumber", err)
	}
	other, err := PageKey("other", 1)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := MetaKey("db")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		key  []byte
	}{
		{"page zero", []byte("sqlite/db/page/\x00\x00\x00\x00")},
		{"short", []byte("sqlite/db/page/\x00\x00\x01")},
		{"long", []byte("sqlite/db/page/\x00\x00\x00\x01\x00")},
		{"other database", other},
		{"prefix of a longer name", []byte("sqlite/dbx/page/\x00\x00\x00")},
		{"meta key", meta},
		{"empty", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParsePageKey("db", tc.key); !errors.Is(err, ErrInvalidPageNumber) {
				t.Fatalf("ParsePageKey(%q) err = %v, want ErrInvalidPageNumber", tc.key, err)
			}
		})
	}
}
