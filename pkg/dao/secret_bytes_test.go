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
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// secretEntity is an entity holding key material in a SecretBytes field.
type secretEntity struct {
	ID    uint64      `json:"id" msgpack:"id"`
	Key   SecretBytes `json:"key" msgpack:"key"`
	Label string      `json:"label" msgpack:"label"`
}

func (e *secretEntity) EntityID() uint64      { return e.ID }
func (e *secretEntity) SetEntityID(id uint64) { e.ID = id }

func newSecretEntity() *secretEntity { return &secretEntity{} }

// secretIndexedEntity indexes its SecretBytes field, which New refuses.
type secretIndexedEntity struct {
	ID  uint64      `json:"id" msgpack:"id"`
	Key SecretBytes `json:"key" msgpack:"key" index:"true"`
}

func (e *secretIndexedEntity) EntityID() uint64      { return e.ID }
func (e *secretIndexedEntity) SetEntityID(id uint64) { e.ID = id }

// secretCompoundEntity names its SecretBytes field in a compound index.
type secretCompoundEntity struct {
	ID    uint64      `json:"id" msgpack:"id"`
	Key   SecretBytes `json:"key" msgpack:"key"`
	Label string      `json:"label" msgpack:"label"`
}

func (e *secretCompoundEntity) EntityID() uint64      { return e.ID }
func (e *secretCompoundEntity) SetEntityID(id uint64) { e.ID = id }
func (e *secretCompoundEntity) CompoundIndexes() []index.CompoundIndexDef {
	return []index.CompoundIndexDef{{Fields: []string{"Label", "Key"}}}
}

// testSecret returns n bytes of a recognisable, non-zero pattern.
func testSecret(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i%251) + 1
	}
	return b
}

func secretBytesEqual(t *testing.T, s SecretBytes, want []byte) {
	t.Helper()
	err := s.WithBytes(func(got []byte) error {
		if !bytes.Equal(got, want) {
			t.Errorf("secret bytes differ: got %d bytes, want %d", len(got), len(want))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithBytes: %v", err)
	}
}

func TestNewSecretBytes_MovesSourceIntoProtectedMemory(t *testing.T) {
	want := testSecret(48)
	src := bytes.Clone(want)

	s, err := NewSecretBytes(src)
	if err != nil {
		t.Fatalf("NewSecretBytes: %v", err)
	}
	defer s.Destroy()

	if !bytes.Equal(src, make([]byte, len(src))) {
		t.Fatal("NewSecretBytes left the heap source unwiped")
	}
	if s.IsZero() || s.Len() != len(want) || s.Buffer() == nil {
		t.Fatalf("IsZero=%v Len=%d Buffer=%v", s.IsZero(), s.Len(), s.Buffer())
	}
	secretBytesEqual(t, s, want)
}

func TestNewSecretBytes_RefusesEmpty(t *testing.T) {
	for _, src := range [][]byte{nil, {}} {
		_, err := NewSecretBytes(src)
		var sizeErr *secure.InvalidSizeError
		if !errors.As(err, &sizeErr) {
			t.Fatalf("NewSecretBytes(%v) = %v, want *secure.InvalidSizeError", src, err)
		}
	}
}

func TestSecretBytesFromBuffer_TakesOwnership(t *testing.T) {
	buf, err := secure.NewBuffer(16)
	if err != nil {
		t.Fatalf("NewBuffer: %v", err)
	}
	s := SecretBytesFromBuffer(buf)
	if s.Buffer() != buf {
		t.Fatal("SecretBytesFromBuffer did not wrap the given buffer")
	}
	if err := s.DestroyErr(); err != nil {
		t.Fatalf("DestroyErr: %v", err)
	}
	if buf.Size() != 0 {
		t.Fatal("Destroy on the SecretBytes did not destroy the buffer it owns")
	}
}

func TestSecretBytes_DestroyWipesEveryCopy(t *testing.T) {
	s, err := NewSecretBytes(testSecret(32))
	if err != nil {
		t.Fatalf("NewSecretBytes: %v", err)
	}
	alias := s
	s.Destroy()
	s.Destroy()

	var nilBuf *secure.NilBufferError
	if err := alias.WithBytes(func([]byte) error { return nil }); !errors.As(err, &nilBuf) {
		t.Fatalf("WithBytes through a copy after Destroy = %v, want *secure.NilBufferError", err)
	}
	if alias.Len() != 0 {
		t.Fatalf("Len after Destroy = %d, want 0", alias.Len())
	}
}

func TestSecretBytes_ZeroValue(t *testing.T) {
	var s SecretBytes
	if !s.IsZero() || s.Len() != 0 || s.Buffer() != nil {
		t.Fatal("the zero value must hold nothing")
	}
	s.Destroy()
	if err := s.DestroyErr(); err != nil {
		t.Fatalf("DestroyErr on the zero value = %v", err)
	}
	var nilBuf *secure.NilBufferError
	if err := s.WithBytes(func([]byte) error { return nil }); !errors.As(err, &nilBuf) {
		t.Fatalf("WithBytes on the zero value = %v, want *secure.NilBufferError", err)
	}
}

func TestSecretBytes_FormattingRedacts(t *testing.T) {
	secret := []byte("formatting-must-not-print-this")
	s, err := NewSecretBytes(bytes.Clone(secret))
	if err != nil {
		t.Fatalf("NewSecretBytes: %v", err)
	}
	defer s.Destroy()
	e := secretEntity{ID: 1, Key: s, Label: "l"}

	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%x", "%q"} {
		out := fmt.Sprintf(verb, e)
		if strings.Contains(out, string(secret)) || strings.Contains(out, fmt.Sprintf("%x", secret)) {
			t.Fatalf("%s printed the secret: %s", verb, out)
		}
		if !strings.Contains(out, "redacted") && verb != "%x" && verb != "%q" {
			t.Fatalf("%s printed no redaction: %s", verb, out)
		}
	}
}

func TestSecretBytes_JSONAndTextRefuse(t *testing.T) {
	s, err := NewSecretBytes(testSecret(8))
	if err != nil {
		t.Fatalf("NewSecretBytes: %v", err)
	}
	defer s.Destroy()

	var refusal SecretCodecUnsupportedError
	if _, err := json.Marshal(&secretEntity{Key: s}); !errors.As(err, &refusal) || refusal.CodecType != string(CodecJSON) {
		t.Fatalf("json.Marshal = %v, want SecretCodecUnsupportedError{json}", err)
	}
	var into secretEntity
	if err := json.Unmarshal([]byte(`{"key":"AAEC"}`), &into); !errors.As(err, &refusal) {
		t.Fatalf("json.Unmarshal = %v, want SecretCodecUnsupportedError", err)
	}
	if _, err := s.MarshalText(); !errors.As(err, &refusal) || refusal.CodecType != codecText {
		t.Fatalf("MarshalText = %v, want SecretCodecUnsupportedError{text}", err)
	}
	if err := into.Key.UnmarshalText([]byte("x")); !errors.As(err, &refusal) {
		t.Fatalf("UnmarshalText = %v, want SecretCodecUnsupportedError", err)
	}
}

// Every compiled codec but MessagePack is refused for an entity holding a
// SecretBytes, at construction rather than at the first Save.
func TestNewCodec_RefusesSecretEntityOnCodecsThatCopy(t *testing.T) {
	refused := 0
	for _, ct := range SupportedCodecs() {
		if secretEncodings[ct] != nil {
			continue
		}
		refused++
		_, err := NewCodec[*secretEntity](ct)
		var created CodecCreationError
		var refusal SecretCodecUnsupportedError
		if !errors.As(err, &created) || !errors.As(err, &refusal) || refusal.CodecType != string(ct) {
			t.Fatalf("NewCodec(%s) for a secret entity = %v, want CodecCreationError wrapping SecretCodecUnsupportedError", ct, err)
		}
	}
	if refused == 0 {
		t.Fatal("no compiled codec was checked for refusal; JSON is always compiled in")
	}
}

func TestNew_RefusesSecretEntityOnTheDefaultCodec(t *testing.T) {
	_, err := New(kvstore.NewMemoryKVStore(), "secret_default", newSecretEntity)
	var refusal SecretCodecUnsupportedError
	if !errors.As(err, &refusal) || refusal.CodecType != string(CodecJSON) {
		t.Fatalf("New with the default codec = %v, want SecretCodecUnsupportedError{json}", err)
	}
}

func TestNewSecretCodec_RefusesCodecWithoutEncoding(t *testing.T) {
	_, err := newSecretCodec[*secretEntity](CodecType("no-such-codec"))
	var refusal SecretCodecUnsupportedError
	if !errors.As(err, &refusal) {
		t.Fatalf("newSecretCodec(unknown) = %v, want SecretCodecUnsupportedError", err)
	}
}

func TestHoldsSecretBytes(t *testing.T) {
	type nested struct{ Inner struct{ K SecretBytes } }
	type recursive struct {
		Next  *recursive
		Plain []byte
	}
	type recursiveSecret struct {
		Next *recursiveSecret
		K    *SecretBytes
	}
	cases := map[string]struct {
		t    reflect.Type
		want bool
	}{
		"direct":           {reflect.TypeFor[SecretBytes](), true},
		"pointer entity":   {reflect.TypeFor[*secretEntity](), true},
		"nested struct":    {reflect.TypeFor[nested](), true},
		"slice":            {reflect.TypeFor[[]SecretBytes](), true},
		"array":            {reflect.TypeFor[[2]SecretBytes](), true},
		"map value":        {reflect.TypeFor[map[string]SecretBytes](), true},
		"recursive secret": {reflect.TypeFor[recursiveSecret](), true},
		"plain entity":     {reflect.TypeFor[*TestEntity](), false},
		"recursive plain":  {reflect.TypeFor[recursive](), false},
		"interface":        {reflect.TypeFor[any](), false},
		"bytes":            {reflect.TypeFor[[]byte](), false},
	}
	for name, c := range cases {
		if got := holdsSecretBytes(c.t); got != c.want {
			t.Errorf("%s: holdsSecretBytes = %v, want %v", name, got, c.want)
		}
	}
}

func TestRefuseSecretIndexes(t *testing.T) {
	eType := reflect.TypeFor[secretEntity]()
	if err := refuseSecretIndexes("e", eType, []string{"Label", "Missing"}); err != nil {
		t.Fatalf("plain fields refused: %v", err)
	}
	var refusal SecretFieldIndexedError
	if err := refuseSecretIndexes("e", eType, []string{"Label", "Key"}); !errors.As(err, &refusal) || refusal.Field != "Key" {
		t.Fatalf("SecretBytes index = %v, want SecretFieldIndexedError{Key}", err)
	}
}

func TestBuildRegistrationFields_OmitsSecretFields(t *testing.T) {
	fields := buildRegistrationFields(reflect.TypeFor[secretEntity](), nil, "ID")
	for _, f := range fields {
		if f.GoName == "Key" {
			t.Fatal("a SecretBytes field was registered as a SQL bridge column")
		}
	}
	if len(fields) != 2 {
		t.Fatalf("registered %d fields, want ID and Label", len(fields))
	}
}

func TestSecretErrors_Messages(t *testing.T) {
	for _, err := range []error{
		SecretCodecUnsupportedError{CodecType: "json"},
		SecretEncodingSizeError{Measured: 3, Encoded: 4},
		SecretFieldIndexedError{EntityType: "e", Field: "Key"},
	} {
		if !strings.HasPrefix(err.Error(), "dao: ") {
			t.Errorf("%T message %q lacks the dao: origin", err, err.Error())
		}
	}
}
