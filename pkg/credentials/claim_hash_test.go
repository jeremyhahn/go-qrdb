package credentials

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// recordingWriter keeps every slice handed to Write, as passed.
type recordingWriter struct{ writes [][]byte }

func (w *recordingWriter) Write(b []byte) (int, error) {
	w.writes = append(w.writes, b)
	return len(b), nil
}

// TestWriteField_HashesTheCallersBuffer: a field's bytes reach the hash as
// the caller's own buffer, not a string or other copy of it, so wiping that
// buffer leaves no plaintext the helper made.
func TestWriteField_HashesTheCallersBuffer(t *testing.T) {
	secret := []byte("plaintext-secret")
	w := &recordingWriter{}
	writeField(w, secret)

	require.Len(t, w.writes, 2)
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(secret)))
	assert.Equal(t, n[:], w.writes[0], "the length prefix")
	require.Len(t, w.writes[1], len(secret))
	assert.Same(t, &secret[0], &w.writes[1][0], "the field was copied before hashing")

	secure.Wipe(secret)
	assert.Equal(t, make([]byte, len(secret)), w.writes[1], "wiping the caller's buffer wipes what was hashed")
}

// TestCredentialRecordClaim_DigestsTheOwnedBuffers: the claim's digests are
// those of the length-prefixed fields read in place from the record, and the
// record's wipe zeroes every buffer the claim read.
func TestCredentialRecordClaim_DigestsTheOwnedBuffers(t *testing.T) {
	rec := credentialRecord{"": []byte("current-secret"), versionPrefix + "1": []byte("old-secret")}
	field := func(h interface{ Write([]byte) (int, error) }, b []byte) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(b)))
		_, _ = h.Write(n[:])
		_, _ = h.Write(b)
	}
	value := sha256.New()
	field(value, []byte("current-secret"))
	all := sha256.New()
	field(all, nil)
	field(all, []byte("current-secret"))
	field(all, []byte(versionPrefix+"1"))
	field(all, []byte("old-secret"))

	claim := rec.claim()
	assert.Equal(t, hex.EncodeToString(value.Sum(nil)), claim.Value)
	assert.Equal(t, hex.EncodeToString(all.Sum(nil)), claim.Digest)

	buffers := [][]byte{rec[""], rec[versionPrefix+"1"]}
	rec.wipe()
	for _, b := range buffers {
		assert.Equal(t, make([]byte, len(b)), b, "a buffer the claim read survived the wipe")
	}
}

// TestSumAndClear_OverwritesTheBufferedTail: the digest is the input's, and
// the hash is then fed a block of zeros, which displaces the partial block
// of input it still buffered.
func TestSumAndClear_OverwritesTheBufferedTail(t *testing.T) {
	input := []byte("short-secret")
	h := sha256.New()
	_, _ = h.Write(input)
	want := sha256.Sum256(input)

	assert.Equal(t, hex.EncodeToString(want[:]), sumAndClear(h))
	cleared := sha256.Sum256(append(bytes.Clone(input), make([]byte, sha256.BlockSize)...))
	assert.Equal(t, cleared[:], h.Sum(nil), "the hash was not fed a block of zeros after its sum")
}
