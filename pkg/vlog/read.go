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

package vlog

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"strings"

	"github.com/transparency-dev/merkle/proof"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// The reads below answer from a log's committed records alone, never from a
// Chain's memory, so any replica's storage answers them identically. A proof
// is a bounded set of stored perfect-subtree hashes the merkle library names
// and, where the proof needs one, rehashes: O(log n) point reads, no walk of
// the tree. Every perfect node ever completed is stored, so a proof is served
// at any size up to the log's.

// MaxEntriesPerGet caps how many entries one Get answers.
const MaxEntriesPerGet = 1000

// Head is a log's head as its committed records hold it.
type Head struct {
	// Size is the number of entries, and Root the tree's root at Size.
	Size uint64
	Root []byte

	// BodiesFrom is the lowest index whose body has not expired.
	BodiesFrom uint64

	// Checkpoint is the latest signed checkpoint, nil when the log has none,
	// and CheckpointSize the tree size it is for.
	Checkpoint     []byte
	CheckpointSize uint64
}

// Entry is one entry of a log as Get answers it.
type Entry struct {
	Index uint64

	// Body is the entry as appended, nil when Expired.
	Body []byte

	// Expired reports that ExpireBodies removed the body; the entry's leaf
	// hash, and every proof over it, remain.
	Expired bool
}

// ReadHead returns id's current size and root and its latest checkpoint.
func ReadHead(r storage.Reader, id LogID) (*Head, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	size, lo, hashes, err := readLogHead(r.Get, id)
	if err != nil {
		return nil, err
	}
	root, err := rangeRoot(id, size, hashes)
	if err != nil {
		return nil, err
	}
	head := &Head{Size: size, Root: root, BodiesFrom: lo}

	prefix := checkpointPrefix(id)
	it := r.NewIterator(&storage.IteratorOptions{LowerBound: prefix, UpperBound: prefixEnd(prefix)})
	if it.SeekToLast() && it.Valid() {
		key := bytes.Clone(it.Key())
		cpSize, ok := uint64Suffix(key, prefix)
		if !ok {
			_ = it.Close()
			return nil, &CorruptTableError{Key: string(key), Reason: "malformed checkpoint key"}
		}
		head.Checkpoint = bytes.Clone(it.Value())
		head.CheckpointSize = cpSize
	}
	if err := it.Error(); err != nil {
		_ = it.Close()
		return nil, &TableReadError{Err: err}
	}
	if err := it.Close(); err != nil {
		return nil, &TableReadError{Err: err}
	}
	return head, nil
}

// ReadCheckpoint returns id's signed checkpoint for a tree of size, nil when
// there is none.
func ReadCheckpoint(r storage.Reader, id LogID, size uint64) ([]byte, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	if _, _, _, err := readLogHead(r.Get, id); err != nil {
		return nil, err
	}
	v, err := r.Get(checkpointKey(id, size))
	if err != nil {
		return nil, &TableReadError{Err: err}
	}
	return v, nil
}

// ReadDescriptor reads the descriptor the log was created with.
func ReadDescriptor(r storage.Reader, id LogID) (*Descriptor, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	return readDescriptor(r.Get, id)
}

// readDescriptor reads and decodes id's descriptor through get.
func readDescriptor(get getFunc, id LogID) (*Descriptor, error) {
	dk := descriptorKey(id)
	raw, err := get(dk)
	if err != nil {
		return nil, &TableReadError{Err: err}
	}
	if raw == nil {
		return nil, &LogNotFoundError{ID: id}
	}
	var desc Descriptor
	if err := json.Unmarshal(raw, &desc); err != nil {
		return nil, &CorruptTableError{Key: string(dk), Reason: "malformed descriptor"}
	}
	return &desc, nil
}

// List returns the names of the logs in one tenant's database, in key order.
func List(r storage.Reader, tenantID, database string) ([]LogID, error) {
	if strings.Contains(tenantID, "/") {
		return nil, &InvalidLogNameError{Field: "tenant", Name: tenantID}
	}
	if database == "" || strings.Contains(database, "/") {
		return nil, &InvalidLogNameError{Field: "database", Name: database}
	}
	scope := []byte(LogTablePrefix + tenantID + "/" + database + "/")
	it := r.NewIterator(&storage.IteratorOptions{LowerBound: scope, UpperBound: prefixEnd(scope)})
	var ids []LogID
	for ok := it.SeekToFirst(); ok && it.Valid(); {
		key := bytes.Clone(it.Key())
		id, prefix, parsed := parseLogPrefix(key)
		if !parsed {
			_ = it.Close()
			return nil, &CorruptTableError{Key: string(key), Reason: "key outside any log's prefix"}
		}
		ids = append(ids, id)
		ok = it.Seek(prefixEnd(prefix))
	}
	if err := it.Error(); err != nil {
		_ = it.Close()
		return nil, &TableReadError{Err: err}
	}
	if err := it.Close(); err != nil {
		return nil, &TableReadError{Err: err}
	}
	return ids, nil
}

// Get returns up to limit of id's entries from index from, fewer at the end
// of the log; a limit outside [1, MaxEntriesPerGet] reads MaxEntriesPerGet.
// from equal to the log's size answers no entries; past it, an
// *IndexOutOfRangeError.
func Get(r storage.Reader, id LogID, from uint64, limit int) ([]Entry, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	size, lo, _, err := readLogHead(r.Get, id)
	if err != nil {
		return nil, err
	}
	if from > size {
		return nil, &IndexOutOfRangeError{Index: from, Size: size}
	}
	if limit < 1 || limit > MaxEntriesPerGet {
		limit = MaxEntriesPerGet
	}
	end := min(size, from+uint64(limit))
	entries := make([]Entry, 0, end-from)
	for index := from; index < min(lo, end); index++ {
		entries = append(entries, Entry{Index: index, Expired: true})
	}

	next := max(from, lo)
	if next >= end {
		return entries, nil
	}
	// A named log's entry i is under its own prefix at i; the audit
	// instance's is the linear chain's event at TreeStart+i. A log under
	// the audit name whose descriptor lacks Audit predates the instance and
	// keeps its own entries.
	prefix, base := entryPrefix(id), uint64(0)
	if IsAuditLog(id) {
		desc, err := readDescriptor(r.Get, id)
		if err != nil {
			return nil, err
		}
		if desc.Audit {
			start := EventRangeStart(id.TenantID, 0)
			prefix, base = start[:len(start)-8], desc.TreeStart
		}
	}
	bodyKey := func(index uint64) []byte {
		return binary.BigEndian.AppendUint64(bytes.Clone(prefix), base+index)
	}
	it := r.NewIterator(&storage.IteratorOptions{LowerBound: bodyKey(next), UpperBound: bodyKey(end)})
	for ok := it.SeekToFirst(); ok && it.Valid(); ok = it.Next() {
		seq, idxOK := uint64Suffix(it.Key(), prefix)
		index := seq - base
		if !idxOK || seq < base || index != next {
			key := string(it.Key())
			_ = it.Close()
			return nil, &CorruptTableError{Key: key, Reason: "entry out of sequence"}
		}
		entries = append(entries, Entry{Index: index, Body: bytes.Clone(it.Value())})
		next++
	}
	if err := it.Error(); err != nil {
		_ = it.Close()
		return nil, &TableReadError{Err: err}
	}
	if err := it.Close(); err != nil {
		return nil, &TableReadError{Err: err}
	}
	if next != end {
		return nil, &CorruptTableError{Key: string(bodyKey(next)), Reason: "missing entry"}
	}
	return entries, nil
}

// Inclusion returns the inclusion proof of id's entry at index in the tree
// of size, which a client checks with the merkle library's
// proof.VerifyInclusion against the root at size.
func Inclusion(r storage.Reader, id LogID, index, size uint64) ([][]byte, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	logSize, _, _, err := readLogHead(r.Get, id)
	if err != nil {
		return nil, err
	}
	if size > logSize {
		return nil, &SizeOutOfRangeError{Size: size, Limit: logSize}
	}
	// The library refuses index >= size, the only way Inclusion fails.
	nodes, err := proof.Inclusion(index, size)
	if err != nil {
		return nil, &IndexOutOfRangeError{Index: index, Size: size}
	}
	return rehash(r, id, nodes)
}

// Consistency returns the consistency proof between id's trees of size1 and
// size2, which a client checks with the merkle library's
// proof.VerifyConsistency against the roots at both sizes.
func Consistency(r storage.Reader, id LogID, size1, size2 uint64) ([][]byte, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	logSize, _, _, err := readLogHead(r.Get, id)
	if err != nil {
		return nil, err
	}
	if size2 > logSize {
		return nil, &SizeOutOfRangeError{Size: size2, Limit: logSize}
	}
	// The library refuses size1 > size2, the only way Consistency fails.
	nodes, err := proof.Consistency(size1, size2)
	if err != nil {
		return nil, &SizeOutOfRangeError{Size: size1, Limit: size2}
	}
	return rehash(r, id, nodes)
}

// rehash reads the stored nodes a proof names and lets the library fold the
// ones that make up its ephemeral node.
func rehash(r storage.Reader, id LogID, nodes proof.Nodes) ([][]byte, error) {
	hashes, err := readNodes(r.Get, id, nodes.IDs)
	if err != nil {
		return nil, err
	}
	out, err := nodes.Rehash(hashes, hasher.HashChildren)
	if err != nil {
		return nil, &CorruptTableError{Key: string(logHeadKey(id)), Reason: err.Error()}
	}
	return out, nil
}

// uint64Suffix decodes the 8-byte big-endian number after prefix in k. ok is
// false when k is not exactly prefix and eight bytes.
func uint64Suffix(k, prefix []byte) (uint64, bool) {
	if !bytes.HasPrefix(k, prefix) || len(k) != len(prefix)+8 {
		return 0, false
	}
	return binary.BigEndian.Uint64(k[len(prefix):]), true
}
