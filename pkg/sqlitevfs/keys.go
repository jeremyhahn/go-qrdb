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
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
)

const (
	// MaxDatabaseNameLen is the longest database name, in bytes.
	MaxDatabaseNameLen = 255

	keyRoot      = "sqlite/"
	metaSuffix   = "/meta"
	pageSegment  = "/page/"
	stageSegment = "/stage/"
	pageNumBytes = 4

	// commitIDLen is the length of a staging commit id: 16 random bytes,
	// hex-encoded.
	commitIDLen = 32
)

// ValidateDatabaseName returns ErrInvalidDatabaseName unless db is a usable
// database name: 1 to MaxDatabaseNameLen bytes, no '/', no NUL or other byte
// below 0x20, and neither "." nor "..".
func ValidateDatabaseName(db string) error {
	if db == "" || len(db) > MaxDatabaseNameLen || db == "." || db == ".." {
		return fmt.Errorf("%w: %q", ErrInvalidDatabaseName, db)
	}
	for i := 0; i < len(db); i++ {
		if c := db[i]; c == '/' || c < 0x20 {
			return fmt.Errorf("%w: %q", ErrInvalidDatabaseName, db)
		}
	}
	return nil
}

// MetaKey returns the key of db's meta record: sqlite/<db>/meta.
func MetaKey(db string) ([]byte, error) {
	if err := ValidateDatabaseName(db); err != nil {
		return nil, err
	}
	return []byte(keyRoot + db + metaSuffix), nil
}

// PagePrefix returns the prefix every page key of db begins with:
// sqlite/<db>/page/.
func PagePrefix(db string) ([]byte, error) {
	if err := ValidateDatabaseName(db); err != nil {
		return nil, err
	}
	return []byte(keyRoot + db + pageSegment), nil
}

// PageKey returns the key of page pgno of db: PagePrefix(db) followed by
// pgno as 4 big-endian bytes, so a byte-ordered range over the prefix lists
// the pages in page order. Page numbers start at 1, as in SQLite; 0 is
// refused with ErrInvalidPageNumber.
func PageKey(db string, pgno uint32) ([]byte, error) {
	prefix, err := PagePrefix(db)
	if err != nil {
		return nil, err
	}
	if pgno == 0 {
		return nil, fmt.Errorf("%w: 0", ErrInvalidPageNumber)
	}
	key := make([]byte, len(prefix)+pageNumBytes)
	copy(key, prefix)
	binary.BigEndian.PutUint32(key[len(prefix):], pgno)
	return key, nil
}

// ParsePageKey returns the page number key encodes for db. A key outside
// db's page prefix, of the wrong length, or encoding page 0 is refused with
// ErrInvalidPageNumber.
func ParsePageKey(db string, key []byte) (uint32, error) {
	prefix, err := PagePrefix(db)
	if err != nil {
		return 0, err
	}
	if len(key) != len(prefix)+pageNumBytes || !bytes.HasPrefix(key, prefix) {
		return 0, fmt.Errorf("%w: key %q is not a page key of %q", ErrInvalidPageNumber, key, db)
	}
	pgno := binary.BigEndian.Uint32(key[len(prefix):])
	if pgno == 0 {
		return 0, fmt.Errorf("%w: 0", ErrInvalidPageNumber)
	}
	return pgno, nil
}

// ValidateCommitID reports whether id is a staging commit id: exactly 32
// lowercase hex characters.
func ValidateCommitID(id string) error {
	if len(id) != commitIDLen {
		return fmt.Errorf("%w: %d characters, want %d", ErrInvalidCommitID, len(id), commitIDLen)
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return fmt.Errorf("%w: %q is not lowercase hex", ErrInvalidCommitID, id)
		}
	}
	return nil
}

// newCommitID returns a fresh staging commit id: 16 bytes from crypto/rand,
// hex-encoded.
func newCommitID() (string, error) {
	var raw [commitIDLen / 2]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("sqlitevfs: new commit id: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

// StagePrefix returns the prefix of every staged page of commit commitID of
// database db: sqlite/<db>/stage/<commit-id>/page/. Staged pages are written
// by an oversized commit before its publish and are never read as pages.
func StagePrefix(db, commitID string) ([]byte, error) {
	if err := ValidateDatabaseName(db); err != nil {
		return nil, err
	}
	if err := ValidateCommitID(commitID); err != nil {
		return nil, err
	}
	return []byte(keyRoot + db + stageSegment + commitID + pageSegment), nil
}

// StageKey returns the staging key of page pgno of commit commitID of
// database db: StagePrefix followed by pgno as 4 big-endian bytes. Page
// number 0 is refused, as in PageKey.
func StageKey(db, commitID string, pgno uint32) ([]byte, error) {
	prefix, err := StagePrefix(db, commitID)
	if err != nil {
		return nil, err
	}
	if pgno == 0 {
		return nil, fmt.Errorf("%w: 0", ErrInvalidPageNumber)
	}
	key := make([]byte, len(prefix)+pageNumBytes)
	copy(key, prefix)
	binary.BigEndian.PutUint32(key[len(prefix):], pgno)
	return key, nil
}
