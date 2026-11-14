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

package objstore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	objcommon "github.com/jeremyhahn/go-objstore/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// newLocalEngine returns an objstore Engine backed by the local provider in a
// fresh temporary directory, which it also returns.
func newLocalEngine(t *testing.T) (*Engine, string) {
	t.Helper()
	dir := t.TempDir()
	e, err := New("local", map[string]string{"path": dir})
	require.NoError(t, err)
	t.Cleanup(func() { _ = e.Close() })
	return e, dir
}

// engines returns one engine per provider whose object-name rules differ:
// memory validates names, local additionally maps them onto a filesystem.
func engines(t *testing.T) map[string]*Engine {
	t.Helper()
	local, _ := newLocalEngine(t)
	return map[string]*Engine{"memory": newMemEngine(t), "local": local}
}

// directThreshold returns the longest key length stored under a direct name.
func directThreshold() int {
	n := 0
	for directNameLen(n+1) <= objcommon.MaxKeyLength {
		n++
	}
	return n
}

// patterned returns an n-byte key cycling through every byte value, seeded
// so distinct seeds give distinct keys.
func patterned(n int, seed byte) []byte {
	k := make([]byte, n)
	for i := range k {
		k[i] = byte(i) + seed
	}
	return k
}

// edgeKeys are keys every one of which a provider would refuse, or misread,
// as a verbatim object name.
var edgeKeys = [][]byte{
	{0x00},
	{0xff},
	{0x00, 0xff, '/', '\\'},
	{0xc3, 0x28}, // invalid UTF-8
	[]byte(".."),
	[]byte("../../etc/passwd"),
	[]byte("/absolute"),
	[]byte(`C:\windows`),
	[]byte("a//b"),
	[]byte("a/"),
	[]byte("tab\there\nnewline\r"),
	[]byte(".tmp-123"),
	[]byte("x.metadata.json"),
	[]byte("qrdb-objstore-format"),
	[]byte("k/"),
	[]byte("h/"),
}

func TestObjectName_DirectIsSegmentedLowercaseHex(t *testing.T) {
	name, hashed := objectName([]byte("k1"), objcommon.MaxKeyLength)
	assert.False(t, hashed)
	assert.Equal(t, "k/6b31_", name)

	// 64 bytes is exactly one full segment; one more byte opens a second.
	name, _ = objectName(bytes.Repeat([]byte{0xab}, 64), objcommon.MaxKeyLength)
	assert.Equal(t, "k/"+strings.Repeat("ab", 64)+"_", name)
	name, _ = objectName(bytes.Repeat([]byte{0xab}, 65), objcommon.MaxKeyLength)
	assert.Equal(t, "k/"+strings.Repeat("ab", 64)+"/ab_", name)
}

func TestObjectName_EveryDirectNameFitsTheProvider(t *testing.T) {
	limit := directThreshold()
	require.Greater(t, limit, 255, "the direct form must cover keys longer than a path component")
	for n := 1; n <= limit; n++ {
		name, hashed := objectName(patterned(n, 0), objcommon.MaxKeyLength)
		require.False(t, hashed, "key of %d bytes", n)
		require.Len(t, name, directNameLen(n))
		require.LessOrEqual(t, len(name), objcommon.MaxKeyLength)
		for _, part := range strings.Split(name, "/") {
			require.LessOrEqual(t, len(part), 255, "path component of a %d-byte key", n)
		}
		require.NoError(t, objcommon.ValidateKey(name))
	}
}

func TestObjectName_HashedBeyondTheProviderLimit(t *testing.T) {
	for _, n := range []int{directThreshold() + 1, 4096, protocol.MaxClientKeyLength} {
		key := patterned(n, 1)
		name, hashed := objectName(key, objcommon.MaxKeyLength)
		require.True(t, hashed, "key of %d bytes", n)
		sum := sha256.Sum256(key)
		assert.Equal(t, "h/"+hex.EncodeToString(sum[:]), name)
		assert.True(t, isHashedName(name))
		require.NoError(t, objcommon.ValidateKey(name))
	}
}

func TestDecodeDirectName_RoundTrips(t *testing.T) {
	keys := append([][]byte{patterned(64, 2), patterned(65, 3), patterned(directThreshold(), 4)}, edgeKeys...)
	for _, key := range keys {
		name, _ := objectName(key, objcommon.MaxKeyLength)
		got, ok := decodeDirectName(name, objcommon.MaxKeyLength)
		require.True(t, ok, "name %q", name)
		assert.Equal(t, key, got)
	}
}

func TestDecodeDirectName_RefusesNonCanonicalNames(t *testing.T) {
	hashedLen := strings.Repeat("ab", directThreshold()+1)
	var spelled []string
	for i := 0; i < len(hashedLen); i += segmentLen {
		spelled = append(spelled, hashedLen[i:min(i+segmentLen, len(hashedLen))])
	}
	for _, name := range []string{
		"",
		"k/",
		"k/_",
		"k/6b31",                                // no terminator
		"k/6B31_",                               // uppercase hex
		"k/6b3_",                                // odd length
		"k/zz_",                                 // not hex
		"k/6b/31_",                              // separator inside a segment
		"x/6b31_",                               // wrong prefix
		"k/.tmp-12345",                          // a provider's temporary file
		"k/" + strings.Repeat("ab", 65) + "_",   // missing a segment separator
		"k/" + strings.Join(spelled, "/") + "_", // a key long enough to be stored hashed
		"k/6b31_/6b31_",                         // a terminator inside the name
	} {
		_, ok := decodeDirectName(name, objcommon.MaxKeyLength)
		assert.False(t, ok, "name %q", name)
	}
}

func TestIsHashedName(t *testing.T) {
	sum := sha256.Sum256([]byte("k"))
	good := "h/" + hex.EncodeToString(sum[:])
	assert.True(t, isHashedName(good))
	assert.False(t, isHashedName(strings.ToUpper(good[:3])+good[3:]))
	assert.False(t, isHashedName(good[:len(good)-1]))
	assert.False(t, isHashedName(good+"0"))
	assert.False(t, isHashedName("k/"+good[2:]))
	assert.False(t, isHashedName("h/.tmp-"+good[9:]))
}

func TestBinaryAndEdgeKeys_RoundTripOnEveryProvider(t *testing.T) {
	for provider, e := range engines(t) {
		t.Run(provider, func(t *testing.T) {
			for i, key := range edgeKeys {
				value := []byte{byte(i), 0x00, 0xff}
				require.NoError(t, e.Set(key, value), "set %q", key)
				got, err := e.Get(key)
				require.NoError(t, err)
				assert.Equal(t, value, got, "get %q", key)
				ok, err := e.Exists(key)
				require.NoError(t, err)
				assert.True(t, ok, "exists %q", key)
			}

			listed, err := e.List(nil)
			require.NoError(t, err)
			assert.ElementsMatch(t, edgeKeys, listed)

			it := e.NewIterator(nil)
			var iterated [][]byte
			for ok := it.SeekToFirst(); ok; ok = it.Next() {
				iterated = append(iterated, append([]byte(nil), it.Key()...))
			}
			require.NoError(t, it.Error())
			require.NoError(t, it.Close())
			require.Len(t, iterated, len(edgeKeys))
			for i := 1; i < len(iterated); i++ {
				assert.Negative(t, bytes.Compare(iterated[i-1], iterated[i]), "iterator must yield byte order")
			}

			for _, key := range edgeKeys {
				require.NoError(t, e.Delete(key))
				got, err := e.Get(key)
				require.NoError(t, err)
				assert.Nil(t, got, "deleted %q", key)
			}
			listed, err = e.List(nil)
			require.NoError(t, err)
			assert.Empty(t, listed)
		})
	}
}

func TestEmptyValue_IsDistinctFromMissing(t *testing.T) {
	for provider, e := range engines(t) {
		t.Run(provider, func(t *testing.T) {
			short, long := []byte{0x00}, patterned(directThreshold()+1, 5)
			for _, key := range [][]byte{short, long} {
				require.NoError(t, e.Set(key, nil))
				got, err := e.Get(key)
				require.NoError(t, err)
				assert.NotNil(t, got)
				assert.Empty(t, got)
			}
		})
	}
}

// TestKeyCap_RoundTripsOnEveryProvider pins that every key the data plane
// accepts — up to protocol.MaxClientKeyLength — is storable on every provider,
// across both object-name forms.
func TestKeyCap_RoundTripsOnEveryProvider(t *testing.T) {
	limit := directThreshold()
	lengths := []int{1, 127, 128, 255, 256, limit, limit + 1, 4096, protocol.MaxClientKeyLength}
	for provider, e := range engines(t) {
		t.Run(provider, func(t *testing.T) {
			var keys [][]byte
			for i, n := range lengths {
				key := patterned(n, byte(i))
				keys = append(keys, key)
				value := []byte{byte(i)}
				require.NoError(t, e.Set(key, value), "set %d-byte key", n)
				got, err := e.Get(key)
				require.NoError(t, err)
				assert.Equal(t, value, got, "get %d-byte key", n)
				ok, err := e.Exists(key)
				require.NoError(t, err)
				assert.True(t, ok)
			}

			listed, err := e.List(nil)
			require.NoError(t, err)
			assert.ElementsMatch(t, keys, listed)

			// A prefix too long to encode as a direct name still finds the
			// hashed keys it prefixes.
			capKey := keys[len(keys)-1]
			byPrefix, err := e.List(capKey[:limit+1])
			require.NoError(t, err)
			assert.Equal(t, [][]byte{capKey}, byPrefix)

			// Iteration merges both name forms into one byte-ordered stream.
			it := e.NewIterator(nil)
			var iterated [][]byte
			for ok := it.SeekToFirst(); ok; ok = it.Next() {
				iterated = append(iterated, append([]byte(nil), it.Key()...))
				assert.Len(t, it.Value(), 1)
			}
			require.NoError(t, it.Error())
			require.NoError(t, it.Close())
			require.Len(t, iterated, len(keys))
			for i := 1; i < len(iterated); i++ {
				assert.Negative(t, bytes.Compare(iterated[i-1], iterated[i]))
			}

			snap, err := e.Snapshot()
			require.NoError(t, err)
			got, err := snap.Get(capKey)
			require.NoError(t, err)
			assert.Equal(t, []byte{byte(len(lengths) - 1)}, got)
			require.NoError(t, snap.Close())

			for _, key := range keys {
				require.NoError(t, e.Delete(key))
				ok, err := e.Exists(key)
				require.NoError(t, err)
				assert.False(t, ok)
			}
		})
	}
}

func TestList_PrefixNarrowsAcrossBothNameForms(t *testing.T) {
	e := newMemEngine(t)
	long := append([]byte{0x01, 0x02}, patterned(directThreshold(), 6)...)
	require.NoError(t, e.Set([]byte{0x01, 0x02, 0x03}, []byte("a")))
	require.NoError(t, e.Set(long, []byte("b")))
	require.NoError(t, e.Set([]byte{0x01, 0x03}, []byte("c")))
	require.NoError(t, e.Set([]byte{0x02}, []byte("d")))

	got, err := e.List([]byte{0x01, 0x02})
	require.NoError(t, err)
	assert.ElementsMatch(t, [][]byte{{0x01, 0x02, 0x03}, long}, got)

	it := e.NewIterator(&storage.IteratorOptions{LowerBound: []byte{0x01, 0x02}, UpperBound: []byte{0x01, 0x03}})
	var bounded [][]byte
	for ok := it.SeekToFirst(); ok; ok = it.Next() {
		bounded = append(bounded, append([]byte(nil), it.Key()...))
	}
	require.NoError(t, it.Close())
	assert.Equal(t, [][]byte{{0x01, 0x02, 0x03}, long}, bounded)
}

func TestList_SkipsNamesThatAreNotRecords(t *testing.T) {
	e := newMemEngine(t)
	require.NoError(t, e.Set([]byte("real"), []byte("v")))
	// go-objstore refuses a Put of a ".tmp-" segment (reserved for a
	// provider's temporary file), so those names are covered by the
	// decodeDirectName and isHashedName cases above, not planted here.
	for _, name := range []string{"k/6B31_", "h/abc", "other/object"} {
		require.NoError(t, e.store.Put(name, strings.NewReader("x")))
	}
	got, err := e.List(nil)
	require.NoError(t, err)
	assert.Equal(t, [][]byte{[]byte("real")}, got)

	it := e.NewIterator(nil)
	require.True(t, it.SeekToFirst())
	assert.Equal(t, []byte("real"), it.Key())
	assert.False(t, it.Next())
	require.NoError(t, it.Close())
}

func TestHashedRecord_CorruptionIsRefused(t *testing.T) {
	key := patterned(directThreshold()+1, 7)
	name, hashed := objectName(key, objcommon.MaxKeyLength)
	require.True(t, hashed)

	cases := map[string]struct {
		body     []byte
		mismatch bool
	}{
		"empty body":          {body: nil},
		"overlong varint":     {body: bytes.Repeat([]byte{0xff}, 11)},
		"truncated key":       {body: []byte{0x80, 0x10, 'a'}},
		"key of another name": {body: []byte{0x01, 'x'}, mismatch: true},
	}
	for label, tc := range cases {
		t.Run(label, func(t *testing.T) {
			e := newMemEngine(t)
			require.NoError(t, e.store.Put(name, bytes.NewReader(tc.body)))

			check := func(err error) {
				t.Helper()
				if tc.mismatch {
					var mismatch *KeyMismatchError
					require.ErrorAs(t, err, &mismatch)
					assert.Equal(t, name, mismatch.Name)
					return
				}
				var corrupt *RecordCorruptError
				require.ErrorAs(t, err, &corrupt)
				assert.Equal(t, name, corrupt.Name)
			}
			_, err := e.Get(key)
			check(err)
			_, err = e.List(nil)
			check(err)
			it := e.NewIterator(nil)
			check(it.Error())
			require.NoError(t, it.Close())
		})
	}
}

// TestGet_KeyMismatchForAnotherKeysRecord covers a well-formed record found
// under a name its key does not hash to: one key's record moved under
// another's name must never be returned as that other key's value.
func TestGet_KeyMismatchForAnotherKeysRecord(t *testing.T) {
	a := patterned(directThreshold()+1, 8)
	b := patterned(directThreshold()+1, 9)
	nameA, _ := objectName(a, objcommon.MaxKeyLength)
	e := newMemEngine(t)
	// b's record placed under a's name does not hash to it.
	require.NoError(t, e.store.Put(nameA, hashedRecord(b, []byte("v"))))
	_, err := e.Get(a)
	var mismatch *KeyMismatchError
	require.ErrorAs(t, err, &mismatch)
	assert.Equal(t, nameA, mismatch.Name)
}

func TestNew_MarksAnEmptyStoreAndReopensIt(t *testing.T) {
	dir := t.TempDir()
	e, err := New("local", map[string]string{"path": dir})
	require.NoError(t, err)
	key := []byte{0x00, 0x01}
	require.NoError(t, e.Set(key, []byte("v")))
	require.NoError(t, e.Close())

	marker, err := os.ReadFile(filepath.Join(dir, formatMarkerName))
	require.NoError(t, err)
	assert.Equal(t, formatMarker(objcommon.MaxKeyLength), string(marker))
	assert.Equal(t, "3\nname-limit=1024\n", string(marker), "the marker records the naming limit")

	reopened, err := New("local", map[string]string{"path": dir})
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close() })
	got, err := reopened.Get(key)
	require.NoError(t, err)
	assert.Equal(t, []byte("v"), got)
}

func TestNew_RefusesAnUnmarkedNonEmptyStore(t *testing.T) {
	dir := t.TempDir()
	// An object named with its key verbatim, as the previous layout wrote it.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "user-key"), []byte("v"), 0o600))

	_, err := New("local", map[string]string{"path": dir})
	var missing *FormatMarkerMissingError
	require.ErrorAs(t, err, &missing)
	assert.Equal(t, "local", missing.Provider)
	assert.Contains(t, err.Error(), formatMarkerName)
	assert.Contains(t, err.Error(), "no migration")
}

func TestNew_RefusesAnotherFormatVersion(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, formatMarkerName), []byte("1"), 0o600))

	_, err := New("local", map[string]string{"path": dir})
	var mismatch *FormatVersionMismatchError
	require.ErrorAs(t, err, &mismatch)
	assert.Equal(t, "1", mismatch.Found)
	assert.Equal(t, formatVersion, mismatch.Want)
	assert.Contains(t, err.Error(), "no migration")
}

func TestErrors_Strings(t *testing.T) {
	assert.Equal(t, "objstore: corrupt record at h/x: why", (&RecordCorruptError{Name: "h/x", Reason: "why"}).Error())
	assert.Equal(t, "objstore: stored key does not match the object name h/x", (&KeyMismatchError{Name: "h/x"}).Error())
	assert.Contains(t, (&FormatMarkerMissingError{Provider: "s3"}).Error(), "the s3 store is not empty")
	assert.Contains(t, (&FormatVersionMismatchError{Found: "9", Want: "2"}).Error(), "version 9 does not match this build's 2")
}
