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

package statemachine

import (
	"sort"

	"github.com/cespare/xxhash/v2"
)

// RecordVisitor receives one record from Backend.Records. The key and value
// slices are only valid for the duration of the call and must not be
// modified.
type RecordVisitor func(key, value []byte) error

// kvRecord is one key-value pair collected for an ordered visit.
type kvRecord struct {
	key   string
	value []byte
}

// visitSorted calls fn for every record in ascending bytewise key order and
// stops at the first error fn returns.
func visitSorted(recs []kvRecord, fn RecordVisitor) error {
	sort.Slice(recs, func(i, j int) bool { return recs[i].key < recs[j].key })
	for _, r := range recs {
		if err := fn([]byte(r.key), r.value); err != nil {
			return err
		}
	}
	return nil
}

// xxhashRecords is the GetHash every backend shares: xxhash over each
// record's key then value, in ascending key order.
func xxhashRecords(records func(RecordVisitor) error) (uint64, error) {
	h := xxhash.New()
	err := records(func(key, value []byte) error {
		_, _ = h.Write(key)
		_, _ = h.Write(value)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return h.Sum64(), nil
}
