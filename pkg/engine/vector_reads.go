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

package engine

import (
	"fmt"
	"strconv"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/vector"
)

// ---------------------------------------------------------------------------
// Vector engine reads: the lookup-side half of vector_handlers.go. Each
// function reads the __vec: keys the apply handlers wrote, through the reader
// the engine's Lookup was given, so a database's vectors are read back from
// the same store (and the same MVCC snapshot) they were written into.
// ---------------------------------------------------------------------------

// lookupVectorNamespace answers service.VectorNamespaceQuery: a
// *service.VectorNamespaceAnswer, or an untyped nil when ns does not exist.
func lookupVectorNamespace(r storage.Reader, ns string) (interface{}, error) {
	rec, err := readNamespaceRecord(r, ns)
	if err != nil || rec == nil {
		return nil, err
	}
	n, err := countVectors(r, ns)
	if err != nil {
		return nil, err
	}
	return &service.VectorNamespaceAnswer{Namespace: ns, Record: *rec, Count: n}, nil
}

// lookupVectorNamespaces answers service.VectorNamespaceListQuery: every
// namespace in ascending name order.
func lookupVectorNamespaces(r storage.Reader) ([]service.VectorNamespaceAnswer, error) {
	prefix := vector.NamespacePrefix()
	it := r.NewIterator(&storage.IteratorOptions{
		LowerBound: prefix,
		UpperBound: prefixEndBytes(prefix),
		OnlyKeys:   true,
	})
	var names []string
	for it.SeekToFirst(); it.Valid(); it.Next() {
		names = append(names, string(it.Key()[len(prefix):]))
	}
	err := it.Error()
	_ = it.Close()
	if err != nil {
		return nil, err
	}
	out := make([]service.VectorNamespaceAnswer, 0, len(names))
	for _, name := range names {
		a, err := lookupVectorNamespace(r, name)
		if err != nil {
			return nil, err
		}
		if a == nil {
			continue
		}
		ans, ok := a.(*service.VectorNamespaceAnswer)
		if !ok {
			return nil, fmt.Errorf("engine: vector namespace %q: unexpected answer type %T", name, a)
		}
		out = append(out, *ans)
	}
	return out, nil
}

// countVectors counts the vectors stored under ns with a keys-only scan.
func countVectors(r storage.Reader, ns string) (uint64, error) {
	prefix := vector.VectorPrefix(ns)
	it := r.NewIterator(&storage.IteratorOptions{
		LowerBound: prefix,
		UpperBound: prefixEndBytes(prefix),
		OnlyKeys:   true,
	})
	defer func() { _ = it.Close() }()
	var n uint64
	for it.SeekToFirst(); it.Valid(); it.Next() {
		n++
	}
	return n, it.Error()
}

// lookupVector answers service.VectorGetQuery: a *vector.VectorRecord, or an
// untyped nil when the vector does not exist.
func lookupVector(r storage.Reader, ns string, id uint64) (interface{}, error) {
	data, err := r.Get(vector.VectorKey(ns, id))
	if err != nil || data == nil {
		return nil, err
	}
	rec, err := vector.DecodeVectorRecord(data)
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// lookupVectorScan answers service.VectorScanQuery: the vectors of q.Namespace
// in ascending ID order from q.FromID, at most q.Limit of them when
// q.Limit > 0.
func lookupVectorScan(r storage.Reader, q service.VectorScanQuery) ([]service.VectorScanItem, error) {
	prefix := vector.VectorPrefix(q.Namespace)
	it := r.NewIterator(&storage.IteratorOptions{
		LowerBound: vector.VectorKey(q.Namespace, q.FromID),
		UpperBound: prefixEndBytes(prefix),
	})
	defer func() { _ = it.Close() }()
	var out []service.VectorScanItem
	for it.SeekToFirst(); it.Valid(); it.Next() {
		if q.Limit > 0 && len(out) >= q.Limit {
			break
		}
		key := append([]byte(nil), it.Key()...)
		value := append([]byte(nil), it.Value()...)
		id, err := strconv.ParseUint(string(key[len(prefix):]), 16, 64)
		if err != nil {
			return nil, err
		}
		rec, err := vector.DecodeVectorRecord(value)
		if err != nil {
			return nil, err
		}
		out = append(out, service.VectorScanItem{ID: id, Record: rec})
	}
	if err := it.Error(); err != nil {
		return nil, err
	}
	return out, nil
}
