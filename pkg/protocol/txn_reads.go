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
	"encoding/binary"

	"github.com/jeremyhahn/go-qrdb/pkg/kv"
)

// A transaction's apply result carries its succeeded flag in Data[0]. When
// the executed branch holds a read (an OpMVCCRange op: a single-key get when
// its range end is empty, a range read otherwise), the flag is followed by
// one read result per executed op, in branch order, so the proposer gets
// every read's key-values back from the same linearizable apply that ran the
// branch's writes:
//
//	[1B succeeded]
//	[4B op count]
//	per op: [8B count][4B kv count]
//	        per kv: [4B key len][key][4B value len][value]
//	                [8B create revision][8B mod revision][8B version][8B lease ID]
//
// A write op's entry is empty (count 0, no key-values). A result of one byte
// is a branch with no read, the shape every transaction had before reads.

// txnReadKVMeta is the width of one encoded key-value's four int64 metadata
// fields.
const txnReadKVMeta = 4 * 8

// TxnReadResult is the answer to one op in a transaction's executed branch.
// Kvs is empty for a write; for a read it holds the matched key-values with
// their full MVCC metadata, and Count the number of keys the read matched.
type TxnReadResult struct {
	Kvs   []*kv.KeyValue
	Count int64
}

// EncodeTransactionResult encodes a transaction's apply result Data: the
// succeeded flag and, when reads is non-nil, one read result per executed op.
func EncodeTransactionResult(succeeded bool, reads []TxnReadResult) ([]byte, error) {
	var flag byte
	if succeeded {
		flag = 1
	}
	if reads == nil {
		return []byte{flag}, nil
	}
	if err := CheckUint32Len(len(reads), "transaction read results"); err != nil {
		return nil, err
	}
	buf := make([]byte, 0, 1+frameSize+len(reads)*(8+frameSize))
	buf = append(buf, flag)
	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(reads)))
	for _, r := range reads {
		if err := CheckUint32Len(len(r.Kvs), "transaction read key-values"); err != nil {
			return nil, err
		}
		buf = binary.LittleEndian.AppendUint64(buf, uint64(r.Count))
		buf = binary.LittleEndian.AppendUint32(buf, uint32(len(r.Kvs)))
		for _, e := range r.Kvs {
			if err := CheckUint32Len(len(e.Key), "transaction read key"); err != nil {
				return nil, err
			}
			if err := CheckUint32Len(len(e.Value), "transaction read value"); err != nil {
				return nil, err
			}
			buf = binary.LittleEndian.AppendUint32(buf, uint32(len(e.Key)))
			buf = append(buf, e.Key...)
			buf = binary.LittleEndian.AppendUint32(buf, uint32(len(e.Value)))
			buf = append(buf, e.Value...)
			buf = binary.LittleEndian.AppendUint64(buf, uint64(e.CreateRevision))
			buf = binary.LittleEndian.AppendUint64(buf, uint64(e.ModRevision))
			buf = binary.LittleEndian.AppendUint64(buf, uint64(e.Version))
			buf = binary.LittleEndian.AppendUint64(buf, uint64(e.LeaseID))
		}
	}
	return buf, nil
}

// DecodeTransactionResult decodes the Data EncodeTransactionResult produced.
// It returns the succeeded flag and the per-op read results, nil when the
// executed branch held no read. Empty data decodes as a failed branch with no
// reads, the shape of a result that carries no flag at all.
func DecodeTransactionResult(data []byte) (bool, []TxnReadResult, error) {
	if len(data) == 0 {
		return false, nil, nil
	}
	succeeded := data[0] == 1
	if len(data) == 1 {
		return succeeded, nil, nil
	}
	off := 1
	if off+frameSize > len(data) {
		return false, nil, &InvalidCommandError{Reason: "txn result: missing op count"}
	}
	opCount, err := decodeCount(binary.LittleEndian.Uint32(data[off:]), len(data)-off-frameSize, "txn result: op")
	if err != nil {
		return false, nil, err
	}
	off += frameSize
	reads := make([]TxnReadResult, 0, preallocSize(opCount))
	for i := 0; i < opCount; i++ {
		if off+8+frameSize > len(data) {
			return false, nil, &InvalidCommandError{Reason: "txn result: truncated op header"}
		}
		count := int64(binary.LittleEndian.Uint64(data[off:]))
		off += 8
		kvCount, cerr := decodeCount(binary.LittleEndian.Uint32(data[off:]), len(data)-off-frameSize, "txn result: key-value")
		if cerr != nil {
			return false, nil, cerr
		}
		off += frameSize
		kvs := make([]*kv.KeyValue, 0, preallocSize(kvCount))
		for j := 0; j < kvCount; j++ {
			e, n, kerr := decodeTxnReadKV(data[off:])
			if kerr != nil {
				return false, nil, kerr
			}
			kvs = append(kvs, e)
			off += n
		}
		reads = append(reads, TxnReadResult{Kvs: kvs, Count: count})
	}
	if off != len(data) {
		return false, nil, &InvalidCommandError{Reason: "txn result: trailing bytes"}
	}
	return succeeded, reads, nil
}

// decodeTxnReadKV decodes one key-value of a transaction read result and
// returns the bytes it consumed. Key and value are copied out of data.
func decodeTxnReadKV(data []byte) (*kv.KeyValue, int, error) {
	off := 0
	field := func(what string) ([]byte, error) {
		if off+frameSize > len(data) {
			return nil, &InvalidCommandError{Reason: "txn result: missing " + what + " length"}
		}
		n, err := decodeLen(binary.LittleEndian.Uint32(data[off:]), len(data)-off-frameSize, "txn result: "+what+" exceeds data")
		if err != nil {
			return nil, err
		}
		off += frameSize
		out := append([]byte(nil), data[off:off+n]...)
		off += n
		return out, nil
	}
	key, err := field("key")
	if err != nil {
		return nil, 0, err
	}
	value, err := field("value")
	if err != nil {
		return nil, 0, err
	}
	if off+txnReadKVMeta > len(data) {
		return nil, 0, &InvalidCommandError{Reason: "txn result: truncated key-value metadata"}
	}
	e := &kv.KeyValue{
		Key:            key,
		Value:          value,
		CreateRevision: int64(binary.LittleEndian.Uint64(data[off:])),
		ModRevision:    int64(binary.LittleEndian.Uint64(data[off+8:])),
		Version:        int64(binary.LittleEndian.Uint64(data[off+16:])),
		LeaseID:        int64(binary.LittleEndian.Uint64(data[off+24:])),
	}
	return e, off + txnReadKVMeta, nil
}
