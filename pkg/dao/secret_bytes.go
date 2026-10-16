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

package dao

import (
	"reflect"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// secretBytesRedacted is what every formatting verb prints for a SecretBytes.
// #nosec G101 -- redaction marker printed in place of the secret, not a credential.
const secretBytesRedacted = "dao.SecretBytes(redacted)"

// codecText names the encoding.TextMarshaler path in a
// SecretCodecUnsupportedError; it is the path TOML and YAML encoders take.
const codecText = "text"

// SecretBytes is an entity field holding secret key material in go-enclave
// protected memory: mlocked, guard-paged pages outside the Go heap, wiped
// when the caller calls Destroy.
//
// The DAO codec carries it without leaving a plaintext copy on the Go heap
// that nothing can wipe. Only codecs that can keep that promise accept an
// entity holding one; every other codec refuses it when the codec (and so
// the DAO) is built, with a CodecCreationError wrapping
// SecretCodecUnsupportedError:
//
//   - MessagePack carries it as one raw bin item. Encoding measures the
//     record, then writes the secret straight from its protected pages into
//     a record allocated at exactly that size, so the record the store
//     receives is the only heap copy and no growing buffer leaves a stale
//     one behind. Decoding reads the bin item straight from the record into
//     a new protected Buffer.
//   - JSON, YAML, TOML, BSON, CBOR and ProtoBuf are refused. JSON would
//     base64 the plaintext through the encoder's pooled buffer; the others
//     encode through pooled or growing buffers this package cannot wipe.
//
// SecretBytes also refuses encoding/json and encoding.TextMarshaler directly,
// so an entity marshalled outside the DAO (into a log line, say) fails
// rather than leaks, and every fmt verb prints a redaction.
//
// The value is a handle: copies share the same protected Buffer, and
// Destroy through any copy wipes it for all of them. A decoded entity owns a
// new Buffer that the caller destroys when done; decoding into an entity
// replaces the handle it held without destroying it. The DAO destroys the
// secrets of every entity it decodes and does not return (a natural-key
// check, a record a query filters out or pages past, the rest of a call that
// fails), and a decode that fails destroys what it allocated, so only an
// entity handed to the caller holds locked memory. The zero value holds
// nothing and encodes as nil.
//
// The field must be declared with this static type (directly, or nested in
// a struct, pointer, slice, array or map). A SecretBytes stored in an
// interface-typed field is invisible when the codec is chosen.
type SecretBytes struct {
	buf *secure.Buffer
}

// NewSecretBytes moves src into protected memory. src is wiped on every
// return path, including failure; a nil or empty src returns
// *secure.InvalidSizeError.
func NewSecretBytes(src []byte) (SecretBytes, error) {
	buf, err := secure.NewBufferFromBytes(src)
	if err != nil {
		return SecretBytes{}, err
	}
	return SecretBytes{buf: buf}, nil
}

// SecretBytesFromBuffer wraps buf, taking ownership of it: Destroy on the
// result destroys buf. It is the way to store material that never touched
// the heap, such as a key generated straight into a secure.NewBuffer.
func SecretBytesFromBuffer(buf *secure.Buffer) SecretBytes {
	return SecretBytes{buf: buf}
}

// Buffer returns the protected Buffer holding the secret, or nil for the
// zero value.
func (s SecretBytes) Buffer() *secure.Buffer {
	return s.buf
}

// WithBytes calls fn with the secret's protected bytes, holding the borrow
// for the call. fn must not retain the slice. It returns
// *secure.NilBufferError for the zero value or once the secret is destroyed.
func (s SecretBytes) WithBytes(fn func([]byte) error) error {
	return s.buf.WithBytes(fn)
}

// Len returns the secret's length, or 0 for the zero value or once it is
// destroyed.
func (s SecretBytes) Len() int {
	return s.buf.Size()
}

// IsZero reports whether s holds no Buffer at all.
func (s SecretBytes) IsZero() bool {
	return s.buf == nil
}

// Destroy wipes and releases the secret. It is idempotent and safe on the
// zero value; a release problem is logged (see secure.LogRelease).
func (s SecretBytes) Destroy() {
	s.buf.Destroy()
}

// DestroyErr is Destroy with go-enclave's release report returned instead of
// logged; see secure.Buffer.DestroyErr.
func (s SecretBytes) DestroyErr() error {
	return s.buf.DestroyErr()
}

// String redacts the secret.
func (s SecretBytes) String() string {
	return secretBytesRedacted
}

// GoString redacts the secret under %#v.
func (s SecretBytes) GoString() string {
	return secretBytesRedacted
}

// MarshalJSON refuses: JSON would carry the secret as base64 built on the
// heap.
func (s SecretBytes) MarshalJSON() ([]byte, error) {
	return nil, SecretCodecUnsupportedError{CodecType: string(CodecJSON)}
}

// UnmarshalJSON refuses, for the same reason as MarshalJSON.
func (s *SecretBytes) UnmarshalJSON([]byte) error {
	return SecretCodecUnsupportedError{CodecType: string(CodecJSON)}
}

// MarshalText refuses: a text encoding is a heap copy of the plaintext.
func (s SecretBytes) MarshalText() ([]byte, error) {
	return nil, SecretCodecUnsupportedError{CodecType: codecText}
}

// UnmarshalText refuses, for the same reason as MarshalText.
func (s *SecretBytes) UnmarshalText([]byte) error {
	return SecretCodecUnsupportedError{CodecType: codecText}
}

// secretEncoding is a codec's SecretBytes-carrying encoding: one that moves
// a SecretBytes field between its protected Buffer and the record with no
// other plaintext copy.
type secretEncoding interface {
	marshal(v any) ([]byte, error)
	unmarshal(data []byte, v any) error
}

// secretEncodings maps each codec that can carry a SecretBytes field to its
// encoding. A codec absent from the map, or mapped to nil because its build
// tag is off, refuses an entity holding one.
var secretEncodings = map[CodecType]secretEncoding{
	CodecMsgPack: secretMsgPackEncoding,
}

// secretCodec is the Codec for an entity type holding a SecretBytes field.
type secretCodec[E any] struct {
	enc       secretEncoding
	codecType CodecType
}

func (c *secretCodec[E]) Marshal(entity E) ([]byte, error) {
	return c.enc.marshal(entity)
}

func (c *secretCodec[E]) Unmarshal(data []byte, entity *E) error {
	return c.enc.unmarshal(data, entity)
}

func (c *secretCodec[E]) Type() CodecType {
	return c.codecType
}

// newSecretCodec builds the Codec for an entity type holding a SecretBytes
// field, refusing a codec that cannot carry one.
func newSecretCodec[E any](ct CodecType) (Codec[E], error) {
	enc := secretEncodings[ct]
	if enc == nil {
		return nil, CodecCreationError{
			CodecType: string(ct),
			Cause:     SecretCodecUnsupportedError{CodecType: string(ct)},
		}
	}
	return &secretCodec[E]{enc: enc, codecType: ct}, nil
}

var secretBytesType = reflect.TypeFor[SecretBytes]()

// secretChildTypes maps each kind that can contain a SecretBytes to the
// types it contains.
var secretChildTypes = map[reflect.Kind]func(reflect.Type) []reflect.Type{
	reflect.Pointer: elemType,
	reflect.Slice:   elemType,
	reflect.Array:   elemType,
	reflect.Map: func(t reflect.Type) []reflect.Type {
		return []reflect.Type{t.Key(), t.Elem()}
	},
	reflect.Struct: func(t reflect.Type) []reflect.Type {
		fields := make([]reflect.Type, t.NumField())
		for i := range fields {
			fields[i] = t.Field(i).Type
		}
		return fields
	},
}

func elemType(t reflect.Type) []reflect.Type {
	return []reflect.Type{t.Elem()}
}

// holdsSecretBytes reports whether a value of type t can contain a
// SecretBytes, statically.
func holdsSecretBytes(t reflect.Type) bool {
	return typeHoldsSecret(t, make(map[reflect.Type]bool))
}

func typeHoldsSecret(t reflect.Type, seen map[reflect.Type]bool) bool {
	if t == secretBytesType {
		return true
	}
	children, ok := secretChildTypes[t.Kind()]
	if !ok || seen[t] {
		return false
	}
	seen[t] = true
	for _, c := range children(t) {
		if typeHoldsSecret(c, seen) {
			return true
		}
	}
	return false
}

// destroySecrets destroys every SecretBytes reachable from v through the
// paths a codec decodes into: exported (and embedded) fields, pointers,
// slices, arrays and maps. It is how the DAO releases the protected memory
// of an entity it decoded for its own use and does not hand to its caller;
// without it, each such decode holds locked memory until the process exits.
func destroySecrets(v any) {
	walkSecrets(reflect.ValueOf(v), make(map[uintptr]bool))
}

func walkSecrets(v reflect.Value, seen map[uintptr]bool) {
	if !v.IsValid() {
		return
	}
	if v.Type() == secretBytesType {
		if !v.CanInterface() {
			return
		}
		if s, ok := v.Interface().(SecretBytes); ok {
			s.Destroy()
		}
		return
	}
	children, ok := secretChildValues[v.Kind()]
	if !ok || !holdsSecretBytes(v.Type()) {
		return
	}
	for _, c := range children(v, seen) {
		walkSecrets(c, seen)
	}
}

// secretChildValues maps each kind that can contain a SecretBytes to the
// values it contains. seen stops a pointer cycle.
var secretChildValues = map[reflect.Kind]func(reflect.Value, map[uintptr]bool) []reflect.Value{
	reflect.Pointer: func(v reflect.Value, seen map[uintptr]bool) []reflect.Value {
		if v.IsNil() || seen[v.Pointer()] {
			return nil
		}
		seen[v.Pointer()] = true
		return []reflect.Value{v.Elem()}
	},
	reflect.Slice: indexedValues,
	reflect.Array: indexedValues,
	reflect.Map: func(v reflect.Value, _ map[uintptr]bool) []reflect.Value {
		values := make([]reflect.Value, 0, 2*v.Len())
		for it := v.MapRange(); it.Next(); {
			values = append(values, it.Key(), it.Value())
		}
		return values
	},
	reflect.Struct: func(v reflect.Value, _ map[uintptr]bool) []reflect.Value {
		t := v.Type()
		values := make([]reflect.Value, 0, t.NumField())
		for i := range t.NumField() {
			if sf := t.Field(i); sf.IsExported() || sf.Anonymous {
				values = append(values, v.Field(i))
			}
		}
		return values
	},
}

func indexedValues(v reflect.Value, _ map[uintptr]bool) []reflect.Value {
	values := make([]reflect.Value, v.Len())
	for i := range values {
		values[i] = v.Index(i)
	}
	return values
}
