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

const testCommitID = "0123456789abcdef0123456789abcdef"

func TestStageKeyLayout(t *testing.T) {
	prefix, err := StagePrefix("app.db", testCommitID)
	if err != nil {
		t.Fatal(err)
	}
	if want := "sqlite/app.db/stage/" + testCommitID + "/page/"; string(prefix) != want {
		t.Fatalf("StagePrefix = %q, want %q", prefix, want)
	}
	key, err := StageKey("app.db", testCommitID, 0x01020304)
	if err != nil {
		t.Fatal(err)
	}
	if want := string(prefix) + "\x01\x02\x03\x04"; string(key) != want {
		t.Fatalf("StageKey = %q, want %q", key, want)
	}
}

func TestStageKeyRefusals(t *testing.T) {
	if _, err := StagePrefix("a/b", testCommitID); !errors.Is(err, ErrInvalidDatabaseName) {
		t.Fatalf("StagePrefix bad db err = %v, want ErrInvalidDatabaseName", err)
	}
	if _, err := StageKey("", testCommitID, 1); !errors.Is(err, ErrInvalidDatabaseName) {
		t.Fatalf("StageKey empty db err = %v, want ErrInvalidDatabaseName", err)
	}
	if _, err := StageKey("app.db", testCommitID, 0); !errors.Is(err, ErrInvalidPageNumber) {
		t.Fatalf("StageKey pgno 0 err = %v, want ErrInvalidPageNumber", err)
	}
	for name, id := range map[string]string{
		"empty":     "",
		"31 chars":  testCommitID[:31],
		"33 chars":  testCommitID + "0",
		"uppercase": strings.ToUpper(testCommitID),
		"non-hex":   "0123456789abcdef0123456789abcdeg",
		"slash":     "0123456789abcdef0123456789abcde/",
	} {
		if err := ValidateCommitID(id); !errors.Is(err, ErrInvalidCommitID) {
			t.Fatalf("ValidateCommitID(%s) err = %v, want ErrInvalidCommitID", name, err)
		}
		if _, err := StageKey("app.db", id, 1); !errors.Is(err, ErrInvalidCommitID) {
			t.Fatalf("StageKey(%s) err = %v, want ErrInvalidCommitID", name, err)
		}
	}
	if err := ValidateCommitID(testCommitID); err != nil {
		t.Fatalf("ValidateCommitID(valid) = %v", err)
	}
	id, err := newCommitID()
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateCommitID(id); err != nil {
		t.Fatalf("newCommitID %q: %v", id, err)
	}
}

func TestStageKeysDisjoint(t *testing.T) {
	dbs := []string{"app.db", "stage", "page", "meta", "a", "app.db.stage"}
	var others [][]byte
	for _, db := range dbs {
		meta, err := MetaKey(db)
		if err != nil {
			t.Fatal(err)
		}
		prefix, err := PagePrefix(db)
		if err != nil {
			t.Fatal(err)
		}
		others = append(others, meta, prefix)
		for _, pgno := range []uint32{1, 2, 1<<32 - 1} {
			key, err := PageKey(db, pgno)
			if err != nil {
				t.Fatal(err)
			}
			others = append(others, key)
		}
	}
	for _, db := range dbs {
		prefix, err := StagePrefix(db, testCommitID)
		if err != nil {
			t.Fatal(err)
		}
		stage := [][]byte{prefix}
		for _, pgno := range []uint32{1, 2, 1<<32 - 1} {
			key, err := StageKey(db, testCommitID, pgno)
			if err != nil {
				t.Fatal(err)
			}
			stage = append(stage, key)
		}
		for _, s := range stage {
			for _, o := range others {
				if bytes.HasPrefix(o, s) || bytes.HasPrefix(s, o) {
					t.Fatalf("stage key %q and page/meta key %q overlap", s, o)
				}
			}
		}
	}
}

func TestStageRootAndWriterLockKey(t *testing.T) {
	root, err := StageRoot("app.db")
	if err != nil || string(root) != "sqlite/app.db/stage/" {
		t.Fatalf("StageRoot = %q, %v", root, err)
	}
	lockKey, err := WriterLockKey("app.db")
	if err != nil || lockKey != "sqlite/app.db/writer" {
		t.Fatalf("WriterLockKey = %q, %v", lockKey, err)
	}
	for _, bad := range []string{"", "a/b", "..", "."} {
		if _, err := StageRoot(bad); !errors.Is(err, ErrInvalidDatabaseName) {
			t.Fatalf("StageRoot(%q) err = %v, want ErrInvalidDatabaseName", bad, err)
		}
		if _, err := WriterLockKey(bad); !errors.Is(err, ErrInvalidDatabaseName) {
			t.Fatalf("WriterLockKey(%q) err = %v, want ErrInvalidDatabaseName", bad, err)
		}
	}

	rootA, err := StageRoot("a")
	if err != nil {
		t.Fatal(err)
	}
	const id = "0123456789abcdef0123456789abcdef"
	for _, db := range []string{"a", "ab"} {
		prefix, err := StagePrefix(db, id)
		if err != nil {
			t.Fatal(err)
		}
		dbRoot, err := StageRoot(db)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.HasPrefix(prefix, dbRoot) {
			t.Fatalf("StagePrefix(%q) = %q does not start with %q", db, prefix, dbRoot)
		}
		page, err := PageKey(db, 1)
		if err != nil {
			t.Fatal(err)
		}
		meta, err := MetaKey(db)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.HasPrefix(page, dbRoot) || bytes.HasPrefix(meta, dbRoot) {
			t.Fatalf("StageRoot(%q) prefixes a page or meta key", db)
		}
		if db == "ab" && (bytes.HasPrefix(prefix, rootA) || bytes.HasPrefix(page, rootA) || bytes.HasPrefix(meta, rootA)) {
			t.Fatal(`StageRoot("a") prefixes a key of database "ab"`)
		}
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
