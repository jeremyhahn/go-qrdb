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
	"errors"
	"testing"
)

func encodeMeta(version, pageSize, pageCount uint32) []byte {
	return encodeMetaGen(version, pageSize, pageCount, 0)
}

func encodeMetaGen(version, pageSize, pageCount uint32, generation uint64) []byte {
	return encodeMetaFull(version, pageSize, pageCount, generation, 0)
}

func encodeMetaFull(version, pageSize, pageCount uint32, generation, changeCounter uint64) []byte {
	buf := make([]byte, 28)
	binary.BigEndian.PutUint32(buf[0:4], version)
	binary.BigEndian.PutUint32(buf[4:8], pageSize)
	binary.BigEndian.PutUint32(buf[8:12], pageCount)
	binary.BigEndian.PutUint64(buf[12:20], generation)
	binary.BigEndian.PutUint64(buf[20:28], changeCounter)
	return buf
}

func TestMetaRoundTrip(t *testing.T) {
	for _, pageSize := range []uint32{512, 1024, 4096, 32768, 65536} {
		for _, pageCount := range []uint32{0, 1, 3, 1<<32 - 1} {
			m := Meta{FormatVersion: FormatVersion, PageSize: pageSize, PageCount: pageCount}
			data, err := m.MarshalBinary()
			if err != nil {
				t.Fatalf("MarshalBinary(%+v): %v", m, err)
			}
			if want := encodeMeta(1, pageSize, pageCount); !bytes.Equal(data, want) {
				t.Fatalf("MarshalBinary(%+v) = %x, want %x", m, data, want)
			}
			var got Meta
			if err := got.UnmarshalBinary(data); err != nil {
				t.Fatalf("UnmarshalBinary: %v", err)
			}
			if got != m {
				t.Fatalf("round trip = %+v, want %+v", got, m)
			}
		}
	}
}

func TestMetaGenerationRoundTrip(t *testing.T) {
	for _, tc := range []struct{ gen, counter uint64 }{
		{1, 0}, {0, 1}, {7, 9}, {1<<32 + 5, 1<<40 + 3}, {1<<64 - 1, 1<<64 - 1},
	} {
		m := Meta{FormatVersion: FormatVersion, PageSize: 4096, PageCount: 3, Generation: tc.gen, ChangeCounter: tc.counter}
		data, err := m.MarshalBinary()
		if err != nil {
			t.Fatalf("MarshalBinary(%+v): %v", m, err)
		}
		if len(data) != 28 {
			t.Fatalf("MarshalBinary(%+v) is %d bytes, want 28", m, len(data))
		}
		if want := encodeMetaFull(1, 4096, 3, tc.gen, tc.counter); !bytes.Equal(data, want) {
			t.Fatalf("MarshalBinary(%+v) = %x, want %x", m, data, want)
		}
		var got Meta
		if err := got.UnmarshalBinary(data); err != nil {
			t.Fatalf("UnmarshalBinary: %v", err)
		}
		if got != m {
			t.Fatalf("round trip = %+v, want %+v", got, m)
		}
	}
}

func TestMetaRefusals(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"short", encodeMeta(1, 4096, 1)[:27]},
		{"no change counter (20 bytes)", encodeMeta(1, 4096, 1)[:20]},
		{"no generation (12 bytes)", encodeMeta(1, 4096, 1)[:12]},
		{"long", append(encodeMeta(1, 4096, 1), 0)},
		{"version 0", encodeMeta(0, 4096, 1)},
		{"version 2", encodeMeta(2, 4096, 1)},
		{"page size 0", encodeMeta(1, 0, 1)},
		{"page size 256", encodeMeta(1, 256, 1)},
		{"page size 131072", encodeMeta(1, 131072, 1)},
		{"page size not a power of two", encodeMeta(1, 4000, 1)},
		{"page size 1536", encodeMeta(1, 1536, 1)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := Meta{FormatVersion: 1, PageSize: 1024, PageCount: 7}
			if err := m.UnmarshalBinary(tc.data); !errors.Is(err, ErrInvalidMeta) {
				t.Fatalf("UnmarshalBinary err = %v, want ErrInvalidMeta", err)
			}
			if m != (Meta{FormatVersion: 1, PageSize: 1024, PageCount: 7}) {
				t.Fatalf("refused UnmarshalBinary changed the record to %+v", m)
			}
		})
	}
	for _, m := range []Meta{
		{FormatVersion: 0, PageSize: 4096},
		{FormatVersion: 2, PageSize: 4096},
		{FormatVersion: 1, PageSize: 0},
		{FormatVersion: 1, PageSize: 3000},
		{FormatVersion: 1, PageSize: 1 << 17},
	} {
		if _, err := m.MarshalBinary(); !errors.Is(err, ErrInvalidMeta) {
			t.Fatalf("MarshalBinary(%+v) err = %v, want ErrInvalidMeta", m, err)
		}
	}
}
