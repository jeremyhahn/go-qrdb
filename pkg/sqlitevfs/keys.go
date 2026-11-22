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
	"encoding/binary"
	"fmt"
)

const (
	// MaxDatabaseNameLen is the longest database name, in bytes.
	MaxDatabaseNameLen = 255

	keyRoot      = "sqlite/"
	metaSuffix   = "/meta"
	pageSegment  = "/page/"
	pageNumBytes = 4
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
