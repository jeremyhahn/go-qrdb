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
	"sync"

	"github.com/vmihailenco/msgpack/v5"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

var (
	_ msgpack.CustomEncoder = SecretBytes{}
	_ msgpack.CustomDecoder = (*SecretBytes)(nil)
)

// secretMsgPackEncoding is MessagePack's SecretBytes-carrying encoding.
var secretMsgPackEncoding secretEncoding = msgpackSecretEncoding{}

// secretDecodes maps each decoder msgpackSecretEncoding.unmarshal is running
// to the Buffers its DecodeMsgpack calls have allocated so far
// (*[]*secure.Buffer). One goroutine runs a decoder, so the slice needs no
// lock; a decoder outside unmarshal (a direct msgpack.Unmarshal) is absent
// and tracks nothing.
var secretDecodes sync.Map

// EncodeMsgpack writes the secret as one raw bin item, straight from its
// protected pages to the encoder's writer: msgpack writes a bin payload with
// a single Write of the slice it is given and copies it nowhere else. The
// zero value encodes as nil; a destroyed secret is refused with
// *secure.NilBufferError rather than stored as empty.
func (s SecretBytes) EncodeMsgpack(enc *msgpack.Encoder) error {
	if s.buf == nil {
		return enc.EncodeNil()
	}
	return s.buf.WithBytes(enc.EncodeBytes)
}

// DecodeMsgpack reads a bin item straight from the record into a new
// protected Buffer, admitted against the process locked-memory budget. nil
// and an empty item decode as the zero value.
func (s *SecretBytes) DecodeMsgpack(dec *msgpack.Decoder) error {
	n, err := dec.DecodeBytesLen()
	if err != nil {
		return err
	}
	if n <= 0 {
		*s = SecretBytes{}
		return nil
	}
	buf, err := secure.NewBuffer(n)
	if err != nil {
		return err
	}
	if err := buf.WithBytes(dec.ReadFull); err != nil {
		buf.Destroy()
		return err
	}
	if decoded, ok := secretDecodes.Load(dec); ok {
		if p, ok := decoded.(*[]*secure.Buffer); ok {
			*p = append(*p, buf)
		}
	}
	*s = SecretBytes{buf: buf}
	return nil
}

// msgpackSecretEncoding encodes in two passes so that no growing buffer
// leaves a stale partial copy of the plaintext behind. msgpack.Marshal
// writes into a bytes.Buffer, and every time that buffer grows the bytes
// already written stay in the abandoned backing array, unwiped. Here the
// first pass only counts bytes, and the second writes into a record
// allocated at exactly that size, which is the one heap copy and is handed
// to the caller. Only entity types holding a SecretBytes pay for the second
// pass.
type msgpackSecretEncoding struct{}

func (msgpackSecretEncoding) marshal(v any) ([]byte, error) {
	enc := msgpack.GetEncoder()
	defer msgpack.PutEncoder(enc)

	var size countingWriter
	enc.Reset(&size)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}

	out := exactWriter{buf: make([]byte, 0, size.n)}
	enc.Reset(&out)
	if err := enc.Encode(v); err != nil {
		secure.Wipe(out.buf)
		return nil, err
	}
	if len(out.buf) != size.n {
		secure.Wipe(out.buf)
		return nil, SecretEncodingSizeError{Measured: size.n, Encoded: len(out.buf)}
	}
	return out.buf, nil
}

// unmarshal reads straight from data, as msgpack.Unmarshal does: the decoder
// uses the bytes.Reader directly rather than through a buffered reader, so
// DecodeMsgpack's ReadFull copies the bin payload from data into the
// protected Buffer and nowhere else.
//
// A decode that fails part-way destroys every Buffer it allocated. The
// caller handed the error has no reliable handle on them: msgpack drops a
// partially decoded map value outright, and a struct field decoded before
// the failure is indistinguishable from one the caller set.
func (msgpackSecretEncoding) unmarshal(data []byte, v any) error {
	dec := msgpack.GetDecoder()
	defer msgpack.PutDecoder(dec)
	dec.UsePreallocateValues(true)
	dec.Reset(bytes.NewReader(data))

	var decoded []*secure.Buffer
	secretDecodes.Store(dec, &decoded)
	err := dec.Decode(v)
	secretDecodes.Delete(dec)
	if err != nil {
		for _, buf := range decoded {
			buf.Destroy()
		}
	}
	return err
}

// countingWriter discards what it is given and counts it.
type countingWriter struct {
	n int
}

func (w *countingWriter) Write(p []byte) (int, error) {
	w.n += len(p)
	return len(p), nil
}

func (w *countingWriter) WriteByte(byte) error {
	w.n++
	return nil
}

// exactWriter appends into buf without ever growing it. A write past its
// capacity is refused, so a record cannot be reallocated part-way through.
type exactWriter struct {
	buf []byte
}

func (w *exactWriter) Write(p []byte) (int, error) {
	if len(p) > cap(w.buf)-len(w.buf) {
		return 0, SecretEncodingSizeError{Measured: cap(w.buf), Encoded: len(w.buf) + len(p)}
	}
	w.buf = append(w.buf, p...)
	return len(p), nil
}

func (w *exactWriter) WriteByte(c byte) error {
	if len(w.buf) == cap(w.buf) {
		return SecretEncodingSizeError{Measured: cap(w.buf), Encoded: len(w.buf) + 1}
	}
	w.buf = append(w.buf, c)
	return nil
}
