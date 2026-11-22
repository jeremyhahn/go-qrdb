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
	"encoding/binary"
	"fmt"
)

const (
	// FormatVersion is the meta record format this package writes and reads.
	FormatVersion uint32 = 1

	// MinPageSize and MaxPageSize bound the page sizes SQLite allows; a page
	// size must also be a power of two.
	MinPageSize uint32 = 512
	MaxPageSize uint32 = 65536

	// metaLen is the encoded meta record length: three big-endian uint32.
	metaLen = 12
)

// Meta is a database's meta record, stored at MetaKey(db).
type Meta struct {
	// FormatVersion is the record format; only FormatVersion (1) is known.
	FormatVersion uint32
	// PageSize is the size of every page in bytes.
	PageSize uint32
	// PageCount is the number of pages; the file size is
	// PageCount*PageSize.
	PageCount uint32
}

// ValidPageSize reports whether n is a page size SQLite allows: a power of
// two in MinPageSize..MaxPageSize.
func ValidPageSize(n uint32) bool {
	return n >= MinPageSize && n <= MaxPageSize && n&(n-1) == 0
}

func (m Meta) validate() error {
	if m.FormatVersion != FormatVersion {
		return fmt.Errorf("%w: unknown format version %d", ErrInvalidMeta, m.FormatVersion)
	}
	if !ValidPageSize(m.PageSize) {
		return fmt.Errorf("%w: page size %d is not a power of two in %d..%d", ErrInvalidMeta, m.PageSize, MinPageSize, MaxPageSize)
	}
	return nil
}

// MarshalBinary encodes m as 12 bytes: format version, page size and page
// count, each a big-endian uint32. An invalid record is refused with
// ErrInvalidMeta.
func (m Meta) MarshalBinary() ([]byte, error) {
	if err := m.validate(); err != nil {
		return nil, err
	}
	buf := make([]byte, metaLen)
	binary.BigEndian.PutUint32(buf[0:4], m.FormatVersion)
	binary.BigEndian.PutUint32(buf[4:8], m.PageSize)
	binary.BigEndian.PutUint32(buf[8:12], m.PageCount)
	return buf, nil
}

// UnmarshalBinary decodes a record MarshalBinary wrote. A wrong length, an
// unknown format version or an invalid page size is refused with
// ErrInvalidMeta and leaves m unchanged.
func (m *Meta) UnmarshalBinary(data []byte) error {
	if len(data) != metaLen {
		return fmt.Errorf("%w: %d bytes, want %d", ErrInvalidMeta, len(data), metaLen)
	}
	decoded := Meta{
		FormatVersion: binary.BigEndian.Uint32(data[0:4]),
		PageSize:      binary.BigEndian.Uint32(data[4:8]),
		PageCount:     binary.BigEndian.Uint32(data[8:12]),
	}
	if err := decoded.validate(); err != nil {
		return err
	}
	*m = decoded
	return nil
}
