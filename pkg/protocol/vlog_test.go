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

package protocol

import (
	"bytes"
	"encoding/binary"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

var testLogID = vlog.LogID{TenantID: "acme", Database: "ledger", Log: "events"}

// splitVerifiableLog checks the op byte and sub-type of cmd and returns the
// decoded tenant id and body.
func splitVerifiableLog(t *testing.T, cmd []byte, wantSubtype byte) (string, []byte) {
	t.Helper()
	if cmd[0] != OpVerifiableLog {
		t.Fatalf("op = %d, want OpVerifiableLog (%d)", cmd[0], OpVerifiableLog)
	}
	subtype, tenantID, body, err := DecodeVerifiableLogCommand(cmd[1:])
	if err != nil {
		t.Fatalf("DecodeVerifiableLogCommand: %v", err)
	}
	if subtype != wantSubtype {
		t.Fatalf("subtype = %d, want %d", subtype, wantSubtype)
	}
	return tenantID, body
}

func TestEncodeVerifiableLogCreate_RoundTrips(t *testing.T) {
	desc := vlog.Descriptor{Origin: "db.example.com/ledger", Hash: vlog.HashSHA256, VerifierKeys: []string{"k1", "k2"}, AllowExpireBodies: true}
	cmd, err := EncodeVerifiableLogCreate(testLogID, desc)
	if err != nil {
		t.Fatalf("EncodeVerifiableLogCreate: %v", err)
	}
	tenantID, body := splitVerifiableLog(t, cmd, VerifiableLogSubtypeCreate)
	id, got, key, err := DecodeVerifiableLogCreate(tenantID, body)
	if err != nil {
		t.Fatalf("DecodeVerifiableLogCreate: %v", err)
	}
	if key != nil {
		t.Fatalf("key = %+v, want none", key)
	}
	if id != testLogID {
		t.Fatalf("id = %v, want %v", id, testLogID)
	}
	if !reflect.DeepEqual(got, desc) {
		t.Fatalf("descriptor = %+v, want %+v", got, desc)
	}
}

func TestEncodeVerifiableLogCreateWithKey_RoundTrips(t *testing.T) {
	key, err := vlog.GenerateSigningKey(vlog.SignatureEd25519, vlog.FirstKeyVersion)
	if err != nil {
		t.Fatalf("GenerateSigningKey: %v", err)
	}
	desc := vlog.Descriptor{Origin: "db.example.com/ledger", Hash: vlog.HashSHA256, Keys: []vlog.HeadKey{headKeyOf(t, key)}}
	cmd, err := EncodeVerifiableLogCreateWithKey(testLogID, desc, key)
	if err != nil {
		t.Fatalf("EncodeVerifiableLogCreateWithKey: %v", err)
	}
	tenantID, body := splitVerifiableLog(t, cmd, VerifiableLogSubtypeCreate)
	id, got, gotKey, err := DecodeVerifiableLogCreate(tenantID, body)
	if err != nil {
		t.Fatalf("DecodeVerifiableLogCreate: %v", err)
	}
	if id != testLogID || !reflect.DeepEqual(got, desc) {
		t.Fatalf("decoded %v %+v, want %v %+v", id, got, testLogID, desc)
	}
	if gotKey == nil || !reflect.DeepEqual(*gotKey, key) {
		t.Fatalf("key = %+v, want %+v", gotKey, key)
	}
}

// headKeyOf returns the descriptor's record of the software key k.
func headKeyOf(t *testing.T, k vlog.SigningKey) vlog.HeadKey {
	t.Helper()
	signer, _, err := k.Signer()
	if err != nil {
		t.Fatalf("Signer: %v", err)
	}
	hk, err := vlog.NewHeadKey(k.Version, vlog.KeyRef(k.Version), signer.Public())
	if err != nil {
		t.Fatalf("NewHeadKey: %v", err)
	}
	return hk
}

func TestEncodeVerifiableLogRotateKey_RoundTripsTheSoftwareRecord(t *testing.T) {
	key, err := vlog.GenerateSigningKey(vlog.SignatureECDSAP256SHA256, 2)
	if err != nil {
		t.Fatalf("GenerateSigningKey: %v", err)
	}
	next := headKeyOf(t, key)
	cmd, err := EncodeVerifiableLogRotateKey(testLogID, next, &key)
	if err != nil {
		t.Fatalf("EncodeVerifiableLogRotateKey: %v", err)
	}
	tenantID, body := splitVerifiableLog(t, cmd, VerifiableLogSubtypeRotateKey)
	id, got, gotKey, err := DecodeVerifiableLogRotateKey(tenantID, body)
	if err != nil {
		t.Fatalf("DecodeVerifiableLogRotateKey: %v", err)
	}
	if id != testLogID || !reflect.DeepEqual(got, next) {
		t.Fatalf("decoded %v %+v, want %v %+v", id, got, testLogID, next)
	}
	if gotKey == nil || !reflect.DeepEqual(*gotKey, key) {
		t.Fatalf("key = %+v, want %+v", gotKey, key)
	}
}

// A provider that keeps its key outside go-qrdb rotates with no record.
func TestEncodeVerifiableLogRotateKey_RoundTripsWithoutARecord(t *testing.T) {
	key, err := vlog.GenerateSigningKey(vlog.SignatureEd25519, 2)
	if err != nil {
		t.Fatalf("GenerateSigningKey: %v", err)
	}
	next := headKeyOf(t, key)
	next.Ref = "hsm:slot-7"
	cmd, err := EncodeVerifiableLogRotateKey(testLogID, next, nil)
	if err != nil {
		t.Fatalf("EncodeVerifiableLogRotateKey: %v", err)
	}
	tenantID, body := splitVerifiableLog(t, cmd, VerifiableLogSubtypeRotateKey)
	id, got, gotKey, err := DecodeVerifiableLogRotateKey(tenantID, body)
	if err != nil || id != testLogID || !reflect.DeepEqual(got, next) || gotKey != nil {
		t.Fatalf("DecodeVerifiableLogRotateKey = %v %+v %+v, %v; want %v %+v and no record", id, got, gotKey, err, testLogID, next)
	}
}

func TestEncodeVerifiableLogRotateKey_RefusesEmptyRecord(t *testing.T) {
	for name, key := range map[string]vlog.SigningKey{
		"version 0": {PKCS8: []byte{1}},
		"no bytes":  {Version: 2},
	} {
		t.Run(name, func(t *testing.T) {
			var ice *InvalidCommandError
			if _, err := EncodeVerifiableLogRotateKey(testLogID, vlog.HeadKey{Version: 2, Ref: "2"}, &key); !errors.As(err, &ice) {
				t.Fatalf("err = %v, want *InvalidCommandError", err)
			}
		})
	}
}

func TestEncodeVerifiableLogCreateWithKey_RefusesEmptyKey(t *testing.T) {
	desc := vlog.Descriptor{Origin: "db.example.com/ledger", Hash: vlog.HashSHA256}
	for name, key := range map[string]vlog.SigningKey{
		"version 0": {PKCS8: []byte{1}},
		"no bytes":  {Version: 1},
	} {
		t.Run(name, func(t *testing.T) {
			var ice *InvalidCommandError
			if _, err := EncodeVerifiableLogCreateWithKey(testLogID, desc, key); !errors.As(err, &ice) {
				t.Fatalf("err = %v, want *InvalidCommandError", err)
			}
		})
	}
}

func TestEncodeVerifiableLogAppend_RoundTrips(t *testing.T) {
	for _, entries := range [][][]byte{
		{[]byte("one")},
		{[]byte("one"), {}, []byte("three")},
	} {
		cmd, err := EncodeVerifiableLogAppend(testLogID, entries)
		if err != nil {
			t.Fatalf("EncodeVerifiableLogAppend: %v", err)
		}
		if len(cmd) != cap(cmd) {
			t.Fatalf("len = %d, cap = %d: size precomputed wrongly", len(cmd), cap(cmd))
		}
		tenantID, body := splitVerifiableLog(t, cmd, VerifiableLogSubtypeAppend)
		id, got, err := DecodeVerifiableLogAppend(tenantID, body)
		if err != nil {
			t.Fatalf("DecodeVerifiableLogAppend: %v", err)
		}
		if id != testLogID {
			t.Fatalf("id = %v, want %v", id, testLogID)
		}
		if len(got) != len(entries) {
			t.Fatalf("entries = %d, want %d", len(got), len(entries))
		}
		for i := range entries {
			if !bytes.Equal(got[i], entries[i]) {
				t.Fatalf("entry %d = %q, want %q", i, got[i], entries[i])
			}
		}
	}
}

func TestEncodeVerifiableLogCheckpoint_RoundTrips(t *testing.T) {
	root := bytes.Repeat([]byte{0xAB}, 32)
	signed := []byte("db.example.com/ledger\n7\nq83vEjRWeJA=\n\n— key sig\n")
	cmd, err := EncodeVerifiableLogCheckpoint(testLogID, 7, root, signed)
	if err != nil {
		t.Fatalf("EncodeVerifiableLogCheckpoint: %v", err)
	}
	if len(cmd) != cap(cmd) {
		t.Fatalf("len = %d, cap = %d: size precomputed wrongly", len(cmd), cap(cmd))
	}
	tenantID, body := splitVerifiableLog(t, cmd, VerifiableLogSubtypeCheckpoint)
	id, size, gotRoot, gotSigned, err := DecodeVerifiableLogCheckpoint(tenantID, body)
	if err != nil {
		t.Fatalf("DecodeVerifiableLogCheckpoint: %v", err)
	}
	if id != testLogID || size != 7 || !bytes.Equal(gotRoot, root) || !bytes.Equal(gotSigned, signed) {
		t.Fatalf("got (%v, %d, %x, %q)", id, size, gotRoot, gotSigned)
	}
}

func TestEncodeVerifiableLogExpireBodies_RoundTrips(t *testing.T) {
	cmd, err := EncodeVerifiableLogExpireBodies(testLogID, 1<<40)
	if err != nil {
		t.Fatalf("EncodeVerifiableLogExpireBodies: %v", err)
	}
	if len(cmd) != cap(cmd) {
		t.Fatalf("len = %d, cap = %d: size precomputed wrongly", len(cmd), cap(cmd))
	}
	tenantID, body := splitVerifiableLog(t, cmd, VerifiableLogSubtypeExpireBodies)
	id, cutoff, err := DecodeVerifiableLogExpireBodies(tenantID, body)
	if err != nil {
		t.Fatalf("DecodeVerifiableLogExpireBodies: %v", err)
	}
	if id != testLogID || cutoff != 1<<40 {
		t.Fatalf("got (%v, %d)", id, cutoff)
	}
}

func TestEncodeVerifiableLog_EmptyTenantIDRoundTrips(t *testing.T) {
	id := vlog.LogID{Database: "system", Log: "audit"}
	cmd, err := EncodeVerifiableLogExpireBodies(id, 3)
	if err != nil {
		t.Fatalf("EncodeVerifiableLogExpireBodies: %v", err)
	}
	tenantID, body := splitVerifiableLog(t, cmd, VerifiableLogSubtypeExpireBodies)
	got, _, err := DecodeVerifiableLogExpireBodies(tenantID, body)
	if err != nil {
		t.Fatalf("DecodeVerifiableLogExpireBodies: %v", err)
	}
	if got != id {
		t.Fatalf("id = %v, want %v", got, id)
	}
}

func TestEncodeVerifiableLog_RejectsInvalidLogID(t *testing.T) {
	for _, id := range []vlog.LogID{
		{TenantID: "a/b", Database: "d", Log: "l"},
		{TenantID: "t", Database: "", Log: "l"},
		{TenantID: "t", Database: "d", Log: "x/y"},
	} {
		_, err := EncodeVerifiableLogExpireBodies(id, 1)
		var ine *vlog.InvalidLogNameError
		if !errors.As(err, &ine) {
			t.Fatalf("%v: err = %v, want *vlog.InvalidLogNameError", id, err)
		}
	}
}

func TestEncodeVerifiableLog_RejectsOversizedNames(t *testing.T) {
	long := strings.Repeat("x", MaxUint16Field+1)
	for _, id := range []vlog.LogID{
		{TenantID: long, Database: "d", Log: "l"},
		{TenantID: "t", Database: long, Log: "l"},
		{TenantID: "t", Database: "d", Log: long},
	} {
		_, err := EncodeVerifiableLogExpireBodies(id, 1)
		var ftl *FieldTooLongError
		if !errors.As(err, &ftl) {
			t.Fatalf("err = %v, want *FieldTooLongError", err)
		}
	}
}

func TestEncodeVerifiableLogAppend_RejectsBatchOutOfBounds(t *testing.T) {
	for _, n := range []int{0, vlog.MaxAppendPerCommand + 1} {
		_, err := EncodeVerifiableLogAppend(testLogID, make([][]byte, n))
		var abe *vlog.AppendBatchError
		if !errors.As(err, &abe) || abe.Count != n {
			t.Fatalf("n = %d: err = %v, want *vlog.AppendBatchError", n, err)
		}
	}
}

func TestEncodeVerifiableLogCheckpoint_RejectsOversizedRoot(t *testing.T) {
	_, err := EncodeVerifiableLogCheckpoint(testLogID, 1, make([]byte, MaxUint16Field+1), nil)
	var ftl *FieldTooLongError
	if !errors.As(err, &ftl) {
		t.Fatalf("err = %v, want *FieldTooLongError", err)
	}
}

func TestDecodeVerifiableLogCommand_RejectsShortPayloads(t *testing.T) {
	for _, payload := range [][]byte{
		nil,
		{VerifiableLogSubtypeAppend},
		{VerifiableLogSubtypeAppend, 0},
		{VerifiableLogSubtypeAppend, 0, 5, 'a', 'b'},
	} {
		_, _, _, err := DecodeVerifiableLogCommand(payload)
		var ice *InvalidCommandError
		if !errors.As(err, &ice) {
			t.Fatalf("payload %v: err = %v, want *InvalidCommandError", payload, err)
		}
	}
}

// logNameBody is the database and log name prefix every body opens with.
func logNameBody(database, log string) []byte {
	b := binary.BigEndian.AppendUint16(nil, uint16(len(database)))
	b = append(b, database...)
	b = binary.BigEndian.AppendUint16(b, uint16(len(log)))
	return append(b, log...)
}

func TestDecodeVerifiableLog_RejectsMalformedBodies(t *testing.T) {
	name := logNameBody("d", "l")
	withName := func(fields ...byte) []byte { return append(append([]byte(nil), name...), fields...) }
	u32 := func(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }

	cases := []struct {
		name   string
		decode func([]byte) error
		body   []byte
	}{
		{"name: empty", decodeCreate, nil},
		{"name: database truncated", decodeCreate, []byte{0, 4, 'd'}},
		{"name: log length missing", decodeCreate, []byte{0, 1, 'd', 0}},
		{"name: log truncated", decodeCreate, []byte{0, 1, 'd', 0, 3, 'l'}},
		{"create: descriptor not JSON", decodeCreate, withName(append(u32(1), '{')...)},
		{"create: descriptor length missing", decodeCreate, withName(0, 0)},
		{"create: descriptor truncated", decodeCreate, withName(append(u32(3), '{', '}')...)},
		{"create: signing key truncated", decodeCreate, withName(append(u32(2), '{', '}', 0, 0, 0, 1)...)},
		{"rotate: key not JSON", decodeRotateKey, withName(append(u32(1), '[')...)},
		{"rotate: key length missing", decodeRotateKey, withName(0, 0)},
		{"rotate: key truncated", decodeRotateKey, withName(append(u32(3), '{', '}')...)},
		{"rotate: signing key truncated", decodeRotateKey, withName(append(u32(2), '{', '}', 0, 0, 0, 2)...)},
		{"append: count missing", decodeAppend, withName(0, 0)},
		{"append: count beyond body", decodeAppend, withName(append(u32(1<<31), 0, 0, 0, 0)...)},
		{"append: entry length missing", decodeAppend, withName(append(u32(2), append(u32(4), 'a', 'b', 'c', 'd')...)...)},
		{"append: entry truncated", decodeAppend, withName(append(u32(1), append(u32(9), 'x')...)...)},
		{"append: trailing bytes", decodeAppend, withName(append(u32(1), append(u32(1), 'x', 'y')...)...)},
		{"checkpoint: size truncated", decodeCheckpoint, withName(0, 0, 0, 0, 0, 0, 0, 1, 0)},
		{"checkpoint: root truncated", decodeCheckpoint, withName(0, 0, 0, 0, 0, 0, 0, 1, 0, 32, 1)},
		{"expire: cutoff short", decodeExpire, withName(0, 0, 0, 0, 0, 0, 1)},
		{"expire: cutoff long", decodeExpire, withName(0, 0, 0, 0, 0, 0, 0, 0, 1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ice *InvalidCommandError
			if err := tc.decode(tc.body); !errors.As(err, &ice) {
				t.Fatalf("err = %v, want *InvalidCommandError", err)
			}
		})
	}
}

func decodeCreate(body []byte) error {
	_, _, _, err := DecodeVerifiableLogCreate("t", body)
	return err
}

func decodeRotateKey(body []byte) error {
	_, _, _, err := DecodeVerifiableLogRotateKey("t", body)
	return err
}

func decodeAppend(body []byte) error {
	_, _, err := DecodeVerifiableLogAppend("t", body)
	return err
}

func decodeCheckpoint(body []byte) error {
	_, _, _, _, err := DecodeVerifiableLogCheckpoint("t", body)
	return err
}

func decodeExpire(body []byte) error {
	_, _, err := DecodeVerifiableLogExpireBodies("t", body)
	return err
}

func TestDecodeVerifiableLogAppend_EntriesDoNotGrowIntoEachOther(t *testing.T) {
	cmd, err := EncodeVerifiableLogAppend(testLogID, [][]byte{[]byte("ab"), []byte("cd")})
	if err != nil {
		t.Fatalf("EncodeVerifiableLogAppend: %v", err)
	}
	tenantID, body := splitVerifiableLog(t, cmd, VerifiableLogSubtypeAppend)
	_, entries, err := DecodeVerifiableLogAppend(tenantID, body)
	if err != nil {
		t.Fatalf("DecodeVerifiableLogAppend: %v", err)
	}
	_ = append(entries[0], 'Z')
	if string(entries[1]) != "cd" {
		t.Fatalf("appending to entry 0 overwrote entry 1: %q", entries[1])
	}
}
