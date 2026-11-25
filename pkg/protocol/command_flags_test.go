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
	"errors"
	"testing"
)

func flagTestOps() (compares, success, failure [][]byte) {
	compares = [][]byte{mustEncode(EncodeCompare([]byte("ck"), 1, 2, []byte("cv"), 1, 2, 3))}
	success = [][]byte{
		mustEncode(EncodeOp(OpMVCCPut, []byte("a"), []byte("y"), nil, 0)),
		mustEncode(EncodeOp(OpMVCCPut, []byte("b"), []byte("x"), nil, 0)),
	}
	failure = [][]byte{mustEncode(EncodeOp(OpMVCCDelete, []byte("f"), nil, nil, 0))}
	return compares, success, failure
}

func TestEncodeTransactionFlags_ZeroIsLegacyBytes(t *testing.T) {
	c, s, f := flagTestOps()
	legacy := mustEncode(EncodeTransaction(c, s, f))
	got := mustEncode(EncodeTransactionFlags(c, s, f, 0))
	if !bytes.Equal(got, legacy) {
		t.Fatalf("flags 0 encoding differs from EncodeTransaction")
	}
	dec, err := DecodeTransaction(got[1:])
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dec.Flags != 0 {
		t.Fatalf("Flags = %#x, want 0", dec.Flags)
	}
}

func TestEncodeTransactionFlags_RoundTrip(t *testing.T) {
	c, s, f := flagTestOps()
	legacy := mustEncode(EncodeTransaction(c, s, f))
	got := mustEncode(EncodeTransactionFlags(c, s, f, CmdFlagOrderedUnique))
	if len(got) != len(legacy)+1 || !bytes.Equal(got[:len(legacy)], legacy) || got[len(legacy)] != CmdFlagOrderedUnique {
		t.Fatalf("flagged encoding is not the legacy bytes plus one trailer byte")
	}
	dec, err := DecodeTransaction(got[1:])
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dec.Flags != CmdFlagOrderedUnique {
		t.Fatalf("Flags = %#x, want %#x", dec.Flags, CmdFlagOrderedUnique)
	}
	if len(dec.Compares) != 1 || len(dec.SuccessOps) != 2 || len(dec.FailureOps) != 1 {
		t.Fatalf("decoded %d/%d/%d, want 1/2/1", len(dec.Compares), len(dec.SuccessOps), len(dec.FailureOps))
	}
	if string(dec.SuccessOps[1].Key) != "b" || string(dec.FailureOps[0].Key) != "f" {
		t.Fatalf("ops not intact: %q %q", dec.SuccessOps[1].Key, dec.FailureOps[0].Key)
	}
}

func TestEncodeBatchFlags_ZeroIsLegacyBytes(t *testing.T) {
	_, s, _ := flagTestOps()
	legacy := mustEncode(EncodeBatch(s))
	got := mustEncode(EncodeBatchFlags(s, 0))
	if !bytes.Equal(got, legacy) {
		t.Fatalf("flags 0 encoding differs from EncodeBatch")
	}
	ops, flags, err := DecodeBatchFlags(got[1:])
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if flags != 0 || len(ops) != 2 {
		t.Fatalf("flags %#x, %d ops; want 0, 2", flags, len(ops))
	}
}

func TestEncodeBatchFlags_RoundTrip(t *testing.T) {
	_, s, _ := flagTestOps()
	legacy := mustEncode(EncodeBatch(s))
	got := mustEncode(EncodeBatchFlags(s, CmdFlagOrderedUnique))
	if len(got) != len(legacy)+1 || !bytes.Equal(got[:len(legacy)], legacy) {
		t.Fatalf("flagged encoding is not the legacy bytes plus one trailer byte")
	}
	ops, flags, err := DecodeBatchFlags(got[1:])
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if flags != CmdFlagOrderedUnique {
		t.Fatalf("flags = %#x, want %#x", flags, CmdFlagOrderedUnique)
	}
	if len(ops) != 2 || string(ops[0].Key) != "a" || string(ops[1].Value) != "x" {
		t.Fatalf("ops not intact: %#v", ops)
	}
	plain, err := DecodeBatch(got[1:])
	if err != nil || len(plain) != 2 {
		t.Fatalf("DecodeBatch on a flagged batch = %d ops, %v", len(plain), err)
	}
}

func TestDecodeTransaction_LongTrailerRefused(t *testing.T) {
	c, s, f := flagTestOps()
	txn := append(mustEncode(EncodeTransaction(c, s, f)), CmdFlagOrderedUnique, 0x00)
	var ice *InvalidCommandError
	if _, err := DecodeTransaction(txn[1:]); !errors.As(err, &ice) {
		t.Fatalf("DecodeTransaction with a two-byte trailer: err = %v, want *InvalidCommandError", err)
	}
	batch := append(mustEncode(EncodeBatch(s)), CmdFlagOrderedUnique, 0x00)
	if _, _, err := DecodeBatchFlags(batch[1:]); !errors.As(err, &ice) {
		t.Fatalf("DecodeBatchFlags with a two-byte trailer: err = %v, want *InvalidCommandError", err)
	}
}
