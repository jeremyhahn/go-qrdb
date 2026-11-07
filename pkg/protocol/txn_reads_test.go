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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kv"
)

func sampleTxnReads() []TxnReadResult {
	return []TxnReadResult{
		{},
		{Count: 1, Kvs: []*kv.KeyValue{{Key: []byte("k"), Value: []byte("v1"), CreateRevision: 3, ModRevision: 7, Version: 2, LeaseID: 9}}},
		{Count: 2, Kvs: []*kv.KeyValue{
			{Key: []byte("a"), Value: nil, CreateRevision: 1, ModRevision: 1, Version: 1},
			{Key: []byte("b"), Value: []byte("bee"), CreateRevision: 4, ModRevision: 5, Version: 2},
		}},
	}
}

func TestTransactionResult_RoundTripsReads(t *testing.T) {
	want := sampleTxnReads()
	data, err := EncodeTransactionResult(true, want)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	succeeded, got, err := DecodeTransactionResult(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !succeeded {
		t.Fatal("succeeded flag lost")
	}
	if len(got) != len(want) {
		t.Fatalf("got %d answers, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Count != want[i].Count || len(got[i].Kvs) != len(want[i].Kvs) {
			t.Fatalf("answer %d: got count %d kvs %d, want %d/%d", i, got[i].Count, len(got[i].Kvs), want[i].Count, len(want[i].Kvs))
		}
		for j, w := range want[i].Kvs {
			g := got[i].Kvs[j]
			if !bytes.Equal(g.Key, w.Key) || !bytes.Equal(g.Value, w.Value) ||
				g.CreateRevision != w.CreateRevision || g.ModRevision != w.ModRevision ||
				g.Version != w.Version || g.LeaseID != w.LeaseID {
				t.Fatalf("answer %d kv %d: got %+v, want %+v", i, j, g, w)
			}
		}
	}
}

func TestTransactionResult_FailedBranchKeepsFlag(t *testing.T) {
	data, err := EncodeTransactionResult(false, sampleTxnReads())
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	succeeded, reads, err := DecodeTransactionResult(data)
	if err != nil || succeeded || len(reads) != 3 {
		t.Fatalf("got (%v, %d reads, %v), want (false, 3, nil)", succeeded, len(reads), err)
	}
}

func TestTransactionResult_NoReadsIsOneByte(t *testing.T) {
	for _, succeeded := range []bool{true, false} {
		data, err := EncodeTransactionResult(succeeded, nil)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		if len(data) != 1 {
			t.Fatalf("no-read result is %d bytes, want 1", len(data))
		}
		got, reads, err := DecodeTransactionResult(data)
		if err != nil || got != succeeded || reads != nil {
			t.Fatalf("got (%v, %v, %v), want (%v, nil, nil)", got, reads, err, succeeded)
		}
	}
}

func TestTransactionResult_EmptyDataIsFailedWithoutReads(t *testing.T) {
	succeeded, reads, err := DecodeTransactionResult(nil)
	if err != nil || succeeded || reads != nil {
		t.Fatalf("got (%v, %v, %v), want (false, nil, nil)", succeeded, reads, err)
	}
}

func TestTransactionResult_EmptyReadsListIsNotOneByte(t *testing.T) {
	data, err := EncodeTransactionResult(true, []TxnReadResult{})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	_, reads, err := DecodeTransactionResult(data)
	if err != nil || reads == nil || len(reads) != 0 {
		t.Fatalf("got (%v, %v), want an empty non-nil answer list", reads, err)
	}
}

func TestTransactionResult_RefusesEveryTruncation(t *testing.T) {
	data, err := EncodeTransactionResult(true, sampleTxnReads())
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	// Every strict prefix longer than the one-byte flag is a truncated
	// answer and must refuse rather than decode short.
	for n := 2; n < len(data); n++ {
		_, _, derr := DecodeTransactionResult(data[:n])
		var ice *InvalidCommandError
		if !errors.As(derr, &ice) {
			t.Fatalf("prefix %d of %d: got %v, want *InvalidCommandError", n, len(data), derr)
		}
	}
}

func TestTransactionResult_RefusesTrailingBytes(t *testing.T) {
	data, err := EncodeTransactionResult(true, sampleTxnReads())
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	_, _, derr := DecodeTransactionResult(append(data, 0))
	var ice *InvalidCommandError
	if !errors.As(derr, &ice) {
		t.Fatalf("got %v, want *InvalidCommandError", derr)
	}
}

func TestTransactionResult_RefusesOversizedCounts(t *testing.T) {
	// An op count, and a key-value count, larger than the bytes behind it
	// could hold refuse before anything is allocated for them.
	opHeader := binary.LittleEndian.AppendUint32([]byte{1}, 1<<30)
	kvHeader := binary.LittleEndian.AppendUint32([]byte{1}, 1)
	kvHeader = binary.LittleEndian.AppendUint64(kvHeader, 1)
	kvHeader = binary.LittleEndian.AppendUint32(kvHeader, 1<<30)
	keyHeader := binary.LittleEndian.AppendUint32([]byte{1}, 1)
	keyHeader = binary.LittleEndian.AppendUint64(keyHeader, 1)
	keyHeader = binary.LittleEndian.AppendUint32(keyHeader, 1)
	keyHeader = binary.LittleEndian.AppendUint32(keyHeader, 1<<30)
	for name, data := range map[string][]byte{"ops": opHeader, "kvs": kvHeader, "key": keyHeader} {
		if _, _, err := DecodeTransactionResult(data); err == nil {
			t.Fatalf("%s: oversized count decoded", name)
		}
	}
}

func TestTransactionResult_DecodeCopiesOutOfData(t *testing.T) {
	data, err := EncodeTransactionResult(true, sampleTxnReads())
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	_, reads, err := DecodeTransactionResult(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	for i := range data {
		data[i] = 0xff
	}
	if string(reads[1].Kvs[0].Key) != "k" || string(reads[1].Kvs[0].Value) != "v1" {
		t.Fatal("decoded key-value aliases the result buffer")
	}
}
