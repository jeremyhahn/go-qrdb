// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

//go:build codec_msgpack

package dao

import (
	"bytes"
	"context"
	"errors"
	"io"
	"runtime"
	"strings"
	"testing"

	"github.com/vmihailenco/msgpack/v5"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// plainSecretEntity is secretEntity with the key as an ordinary []byte: the
// shape msgpack.Marshal is measured against.
type plainSecretEntity struct {
	ID    uint64 `msgpack:"id"`
	Key   []byte `msgpack:"key"`
	Label string `msgpack:"label"`
}

// copySecretSize is large enough that one plaintext-sized heap allocation
// dwarfs every fixed cost of an encode or decode, and small enough to fit
// the locked-memory budget of a default RLIMIT_MEMLOCK.
const copySecretSize = 64 << 10

func newSecretMsgPackCodec(t testing.TB) Codec[*secretEntity] {
	t.Helper()
	c, err := NewCodec[*secretEntity](CodecMsgPack)
	if err != nil {
		t.Fatalf("NewCodec(msgpack): %v", err)
	}
	if c.Type() != CodecMsgPack {
		t.Fatalf("Type = %s, want msgpack", c.Type())
	}
	return c
}

func newSecretEntityWith(t testing.TB, secret []byte) *secretEntity {
	t.Helper()
	s, err := NewSecretBytes(bytes.Clone(secret))
	if err != nil {
		t.Fatalf("NewSecretBytes: %v", err)
	}
	return &secretEntity{ID: 7, Key: s, Label: "signing-key"}
}

// heapBytesPerOp is the Go heap allocated per call of fn, averaged over runs
// after one warm-up call fills the codec's pools.
func heapBytesPerOp(runs int, fn func()) uint64 {
	fn()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range runs {
		fn()
	}
	runtime.ReadMemStats(&after)
	return (after.TotalAlloc - before.TotalAlloc) / uint64(runs)
}

// The issue's first verification: an entity round-trips through the DAO
// with its secret field, and the decoded value can be wiped.
func TestSecretBytes_RoundTripsThroughTheDAOAndWipes(t *testing.T) {
	ctx := context.Background()
	d, err := New(kvstore.NewMemoryKVStore(), "secret_roundtrip", newSecretEntity, WithCodec(CodecMsgPack))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	secret := testSecret(32)
	e := newSecretEntityWith(t, secret)
	defer e.Key.Destroy()
	if err := d.Save(ctx, e); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := d.Get(ctx, e.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Label != e.Label || got.Key.Buffer() == e.Key.Buffer() {
		t.Fatalf("decoded entity = %+v; want its own Buffer and the saved label", got)
	}
	secretBytesEqual(t, got.Key, secret)

	got.Key.Destroy()
	var nilBuf *secure.NilBufferError
	if err := got.Key.WithBytes(func([]byte) error { return nil }); !errors.As(err, &nilBuf) {
		t.Fatalf("WithBytes after Destroy = %v, want *secure.NilBufferError", err)
	}
	secretBytesEqual(t, e.Key, secret)
}

// The bin payload is the raw secret, byte-identical to msgpack's own
// encoding of the same bytes as a []byte field: no base64, no framing of
// its own.
func TestSecretBytes_MsgPackEncodesRawBytes(t *testing.T) {
	c := newSecretMsgPackCodec(t)
	secret := testSecret(300)
	e := newSecretEntityWith(t, secret)
	defer e.Key.Destroy()

	record, err := c.Marshal(e)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want, err := msgpack.Marshal(&plainSecretEntity{ID: e.ID, Key: secret, Label: e.Label})
	if err != nil {
		t.Fatalf("msgpack.Marshal: %v", err)
	}
	if !bytes.Equal(record, want) {
		t.Fatalf("record differs from msgpack's own []byte encoding:\n got %x\nwant %x", record, want)
	}
	if len(record) != cap(record) {
		t.Fatalf("record len %d cap %d: it was not allocated at its exact size", len(record), cap(record))
	}
}

func TestSecretBytes_MsgPackZeroValueRoundTrips(t *testing.T) {
	c := newSecretMsgPackCodec(t)
	record, err := c.Marshal(&secretEntity{ID: 3, Label: "empty"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got := newSecretEntity()
	if err := c.Unmarshal(record, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !got.Key.IsZero() || got.Label != "empty" {
		t.Fatalf("decoded %+v, want a zero SecretBytes", got)
	}

	// An empty bin item from a foreign writer decodes as the zero value too.
	foreign, err := msgpack.Marshal(&plainSecretEntity{ID: 4, Key: []byte{}})
	if err != nil {
		t.Fatalf("msgpack.Marshal: %v", err)
	}
	got = newSecretEntity()
	if err := c.Unmarshal(foreign, &got); err != nil || !got.Key.IsZero() {
		t.Fatalf("empty bin = %+v, %v; want the zero value", got, err)
	}
}

func TestSecretBytes_MsgPackRefusesDestroyedSecret(t *testing.T) {
	c := newSecretMsgPackCodec(t)
	e := newSecretEntityWith(t, testSecret(16))
	e.Key.Destroy()
	var nilBuf *secure.NilBufferError
	if _, err := c.Marshal(e); !errors.As(err, &nilBuf) {
		t.Fatalf("Marshal of a destroyed secret = %v, want *secure.NilBufferError", err)
	}
}

func TestSecretBytes_MsgPackDecodeRefusesTruncatedRecord(t *testing.T) {
	c := newSecretMsgPackCodec(t)
	e := newSecretEntityWith(t, testSecret(64))
	defer e.Key.Destroy()
	record, err := c.Marshal(e)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	// Cut the record inside the bin payload.
	cut := bytes.Index(record, testSecret(64)) + 10
	got := newSecretEntity()
	if err := c.Unmarshal(record[:cut], &got); err == nil {
		t.Fatal("a record truncated inside the secret decoded without error")
	}
	if !got.Key.IsZero() {
		t.Fatal("a failed decode left a half-filled secret in the entity")
	}
	// A key that is not a bin or string item is refused by msgpack.
	bad, err := msgpack.Marshal(map[string]any{"key": 5})
	if err != nil {
		t.Fatalf("msgpack.Marshal: %v", err)
	}
	if err := c.Unmarshal(bad, &got); err == nil {
		t.Fatal("an integer decoded into a SecretBytes")
	}
}

// The issue's second verification, decode side: the secret goes from the
// record into protected memory and never into a Go heap allocation. A
// decode that copied the plaintext through the heap even once would
// allocate at least copySecretSize per op.
func TestSecretBytes_MsgPackDecodeMakesNoHeapCopy(t *testing.T) {
	c := newSecretMsgPackCodec(t)
	e := newSecretEntityWith(t, testSecret(copySecretSize))
	defer e.Key.Destroy()
	record, err := c.Marshal(e)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	perOp := heapBytesPerOp(20, func() {
		got := newSecretEntity()
		if err := c.Unmarshal(record, &got); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		got.Key.Destroy()
	})
	if perOp >= copySecretSize/4 {
		t.Fatalf("decode allocated %d heap bytes per op for a %d-byte secret: the plaintext went through the heap", perOp, copySecretSize)
	}

	// The control: decoding the same record into a []byte field does copy
	// it onto the heap, so the bound above can fail.
	control := heapBytesPerOp(20, func() {
		var plain plainSecretEntity
		if err := msgpack.Unmarshal(record, &plain); err != nil {
			t.Fatalf("msgpack.Unmarshal: %v", err)
		}
	})
	if control < copySecretSize {
		t.Fatalf("control decode allocated %d bytes per op; the measurement cannot see a heap copy", control)
	}
}

// The issue's second verification, encode side: the only plaintext-sized
// heap allocation is the record itself. msgpack.Marshal of the same shape
// grows its buffer when the label after the secret outruns its slack, and
// abandons a copy of the secret in the old backing array, which is what the
// control shows.
func TestSecretBytes_MsgPackEncodeAllocatesOnlyTheRecord(t *testing.T) {
	c := newSecretMsgPackCodec(t)
	secret := testSecret(copySecretSize)
	e := newSecretEntityWith(t, secret)
	defer e.Key.Destroy()
	e.Label = strings.Repeat("l", copySecretSize/4)

	var recordLen int
	perOp := heapBytesPerOp(20, func() {
		record, err := c.Marshal(e)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		recordLen = len(record)
	})
	if perOp >= uint64(recordLen)+copySecretSize/4 {
		t.Fatalf("encode allocated %d heap bytes per op for a %d-byte record: a second plaintext copy was made", perOp, recordLen)
	}

	plain := &plainSecretEntity{ID: e.ID, Key: secret, Label: e.Label}
	control := heapBytesPerOp(20, func() {
		if _, err := msgpack.Marshal(plain); err != nil {
			t.Fatalf("msgpack.Marshal: %v", err)
		}
	})
	if control < uint64(recordLen)+copySecretSize {
		t.Fatalf("control encode allocated %d bytes per op; the measurement cannot see an abandoned copy", control)
	}
}

func TestNew_RefusesAnIndexOverASecretField(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	var refusal SecretFieldIndexedError

	_, err := New(store, "secret_indexed", func() *secretIndexedEntity { return &secretIndexedEntity{} }, WithCodec(CodecMsgPack))
	if !errors.As(err, &refusal) || refusal.Field != "Key" {
		t.Fatalf("New with an index tag on a SecretBytes = %v, want SecretFieldIndexedError{Key}", err)
	}
	_, err = New(store, "secret_compound", func() *secretCompoundEntity { return &secretCompoundEntity{} }, WithCodec(CodecMsgPack))
	if !errors.As(err, &refusal) || refusal.Field != "Key" {
		t.Fatalf("New with a compound index over a SecretBytes = %v, want SecretFieldIndexedError{Key}", err)
	}
}

func TestExactWriter_NeverGrows(t *testing.T) {
	w := exactWriter{buf: make([]byte, 0, 3)}
	if _, err := w.Write([]byte{1, 2}); err != nil {
		t.Fatalf("Write within capacity: %v", err)
	}
	var sizeErr SecretEncodingSizeError
	if _, err := w.Write([]byte{3, 4}); !errors.As(err, &sizeErr) || sizeErr.Measured != 3 || sizeErr.Encoded != 4 {
		t.Fatalf("Write past capacity = %v, want SecretEncodingSizeError{3 4}", err)
	}
	if err := w.WriteByte(3); err != nil {
		t.Fatalf("WriteByte within capacity: %v", err)
	}
	if err := w.WriteByte(4); !errors.As(err, &sizeErr) {
		t.Fatalf("WriteByte past capacity = %v, want SecretEncodingSizeError", err)
	}
	if !bytes.Equal(w.buf, []byte{1, 2, 3}) || cap(w.buf) != 3 {
		t.Fatalf("buf = %v cap %d, want [1 2 3] cap 3", w.buf, cap(w.buf))
	}
}

func TestCountingWriter_Counts(t *testing.T) {
	var w countingWriter
	if n, err := w.Write([]byte{1, 2, 3}); n != 3 || err != nil {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if err := w.WriteByte(4); err != nil {
		t.Fatalf("WriteByte: %v", err)
	}
	if w.n != 4 {
		t.Fatalf("counted %d, want 4", w.n)
	}
	var _ io.Writer = &w
}

// driftingField encodes one byte more (or fewer) each time it is encoded:
// an entity mutated between the measuring pass and the writing pass.
type driftingField struct {
	calls *int
	step  int
}

func (f driftingField) EncodeMsgpack(enc *msgpack.Encoder) error {
	*f.calls++
	return enc.EncodeBytes(make([]byte, 8+*f.calls*f.step))
}

// The measuring pass and the writing pass must agree; when they do not, the
// partial record is wiped and refused rather than grown or truncated.
func TestMsgPackSecretEncoding_RefusesAnEncodingThatChangesSize(t *testing.T) {
	for name, step := range map[string]int{"grew": 1, "shrank": -1} {
		calls := 0
		_, err := secretMsgPackEncoding.marshal(driftingField{calls: &calls, step: step})
		var sizeErr SecretEncodingSizeError
		if !errors.As(err, &sizeErr) {
			t.Fatalf("%s: marshal = %v, want SecretEncodingSizeError", name, err)
		}
	}
	if _, err := secretMsgPackEncoding.marshal(make(chan int)); err == nil {
		t.Fatal("a channel encoded without error")
	}
}

func BenchmarkSecretBytes_MsgPackMarshal(b *testing.B) {
	c := newSecretMsgPackCodec(b)
	e := newSecretEntityWith(b, testSecret(4096))
	defer e.Key.Destroy()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := c.Marshal(e); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSecretBytes_MsgPackUnmarshal(b *testing.B) {
	c := newSecretMsgPackCodec(b)
	e := newSecretEntityWith(b, testSecret(4096))
	defer e.Key.Destroy()
	record, err := c.Marshal(e)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		got := newSecretEntity()
		if err := c.Unmarshal(record, &got); err != nil {
			b.Fatal(err)
		}
		got.Key.Destroy()
	}
}
