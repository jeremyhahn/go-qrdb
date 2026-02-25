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

package mvcc

import (
	"bytes"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// Evaluate checks whether the given compare precondition holds for the
// provided key-value. If kv is nil the key is treated as non-existent
// (all numeric fields are zero and value is nil).
func Evaluate(cmp *service.Compare, kv *service.KeyValue) bool {
	var (
		version        int64
		createRevision int64
		modRevision    int64
		value          []byte
	)
	if kv != nil {
		version = kv.Version
		createRevision = kv.CreateRevision
		modRevision = kv.ModRevision
		value = kv.Value
	}

	switch cmp.Target {
	case service.CompareVersion:
		return compareInt64(version, cmp.Version, cmp.Result)
	case service.CompareCreateRevision:
		return compareInt64(createRevision, cmp.CreateRevision, cmp.Result)
	case service.CompareModRevision:
		return compareInt64(modRevision, cmp.ModRevision, cmp.Result)
	case service.CompareValue:
		return compareBytes(value, cmp.Value, cmp.Result)
	default:
		return false
	}
}

// compareInt64 evaluates a numeric comparison.
func compareInt64(actual, expected int64, op service.CompareResult) bool {
	switch op {
	case service.CompareEqual:
		return actual == expected
	case service.CompareNotEqual:
		return actual != expected
	case service.CompareGreater:
		return actual > expected
	case service.CompareLess:
		return actual < expected
	default:
		return false
	}
}

// compareBytes evaluates a byte slice comparison.
func compareBytes(actual, expected []byte, op service.CompareResult) bool {
	cmp := bytes.Compare(actual, expected)
	switch op {
	case service.CompareEqual:
		return cmp == 0
	case service.CompareNotEqual:
		return cmp != 0
	case service.CompareGreater:
		return cmp > 0
	case service.CompareLess:
		return cmp < 0
	default:
		return false
	}
}
