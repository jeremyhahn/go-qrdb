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

// Raft-backed MVCC compare operations from QRDB.
// These provide extended comparison operators (>=, <=) and lease comparison
// beyond what the base qrdb compare.go offers.

package mvcc

import (
	"bytes"
	"fmt"

	"github.com/jeremyhahn/go-qrdb/pkg/api"
)

// RaftCompareTarget specifies what property of a key to compare.
type RaftCompareTarget int

const (
	RaftCompareVersion RaftCompareTarget = iota // Compare key version
	RaftCompareCreate                           // Compare create revision
	RaftCompareMod                              // Compare mod revision
	RaftCompareValue                            // Compare key value
	RaftCompareLease                            // Compare lease ID
)

// RaftCompareResult specifies the comparison operator.
type RaftCompareResult int

const (
	RaftCompareEqual        RaftCompareResult = iota // ==
	RaftCompareNotEqual                              // !=
	RaftCompareGreater                               // >
	RaftCompareLess                                  // <
	RaftCompareGreaterEqual                          // >=
	RaftCompareLessEqual                             // <=
)

// RaftCompare represents a comparison operation in a Raft-backed transaction.
// It checks a condition against a key's metadata or value.
type RaftCompare struct {
	Key      []byte
	Target   RaftCompareTarget
	Result   RaftCompareResult
	Value    interface{} // For RaftCompareValue target
	Revision int64       // For RaftCompareVersion/Create/Mod/Lease targets
}

// String returns a human-readable representation of the comparison.
func (c *RaftCompare) String() string {
	targetStr := "unknown"
	switch c.Target {
	case RaftCompareVersion:
		targetStr = "version"
	case RaftCompareCreate:
		targetStr = "create"
	case RaftCompareMod:
		targetStr = "mod"
	case RaftCompareValue:
		targetStr = "value"
	case RaftCompareLease:
		targetStr = "lease"
	}

	resultStr := "unknown"
	switch c.Result {
	case RaftCompareEqual:
		resultStr = "=="
	case RaftCompareNotEqual:
		resultStr = "!="
	case RaftCompareGreater:
		resultStr = ">"
	case RaftCompareLess:
		resultStr = "<"
	case RaftCompareGreaterEqual:
		resultStr = ">="
	case RaftCompareLessEqual:
		resultStr = "<="
	}

	valueStr := fmt.Sprintf("%v", c.Value)
	if c.Target != RaftCompareValue {
		valueStr = fmt.Sprintf("%d", c.Revision)
	}

	return fmt.Sprintf("%s.%s %s %s", string(c.Key), targetStr, resultStr, valueStr)
}

// evaluate checks if the comparison holds true for the given KeyValue.
// Returns true if the comparison succeeds, false otherwise.
func (c *RaftCompare) evaluate(kv *RaftKeyValue) (bool, error) {
	// Handle missing key
	if kv == nil {
		// For comparisons, missing key means:
		// - version/create/mod/lease are 0
		// - value is nil
		kv = &RaftKeyValue{
			Key:            c.Key,
			CreateRevision: 0,
			ModRevision:    0,
			Version:        0,
			LeaseID:        0,
			Value:          nil,
		}
	}

	// Get the value to compare against
	var actual int64
	var actualValue []byte

	switch c.Target {
	case RaftCompareVersion:
		actual = kv.Version
	case RaftCompareCreate:
		actual = kv.CreateRevision
	case RaftCompareMod:
		actual = kv.ModRevision
	case RaftCompareLease:
		actual = kv.LeaseID
	case RaftCompareValue:
		actualValue = kv.Value
	default:
		return false, api.NewUnknownCompareTargetError("mvcc.RaftCompare.evaluate", int(c.Target))
	}

	// Perform the comparison
	if c.Target == RaftCompareValue {
		return c.compareValue(actualValue)
	}

	return c.compareRevision(actual)
}

// compareRevision compares an integer value (version/revision/lease).
func (c *RaftCompare) compareRevision(actual int64) (bool, error) {
	expected := c.Revision

	switch c.Result {
	case RaftCompareEqual:
		return actual == expected, nil
	case RaftCompareNotEqual:
		return actual != expected, nil
	case RaftCompareGreater:
		return actual > expected, nil
	case RaftCompareLess:
		return actual < expected, nil
	case RaftCompareGreaterEqual:
		return actual >= expected, nil
	case RaftCompareLessEqual:
		return actual <= expected, nil
	default:
		return false, api.NewUnknownCompareResultError("mvcc.RaftCompare.compareRevision", int(c.Result))
	}
}

// compareValue compares a byte slice value.
func (c *RaftCompare) compareValue(actual []byte) (bool, error) {
	// Convert expected value to []byte
	var expected []byte
	switch v := c.Value.(type) {
	case []byte:
		expected = v
	case string:
		expected = []byte(v)
	case nil:
		expected = nil
	default:
		return false, api.NewUnsupportedValueTypeError("mvcc.RaftCompare.compareValue", fmt.Sprintf("%T", c.Value))
	}

	// Compare byte slices
	cmp := bytes.Compare(actual, expected)

	switch c.Result {
	case RaftCompareEqual:
		return cmp == 0, nil
	case RaftCompareNotEqual:
		return cmp != 0, nil
	case RaftCompareGreater:
		return cmp > 0, nil
	case RaftCompareLess:
		return cmp < 0, nil
	case RaftCompareGreaterEqual:
		return cmp >= 0, nil
	case RaftCompareLessEqual:
		return cmp <= 0, nil
	default:
		return false, api.NewUnknownCompareResultError("mvcc.RaftCompare.compareValue", int(c.Result))
	}
}

// Validate checks if the RaftCompare is well-formed.
func (c *RaftCompare) Validate() error {
	if len(c.Key) == 0 {
		return api.NewValidationError("mvcc.RaftCompare.Validate", "compare key cannot be empty")
	}

	switch c.Target {
	case RaftCompareVersion, RaftCompareCreate, RaftCompareMod, RaftCompareLease:
		// Revision-based comparisons
		if c.Revision < 0 {
			return api.NewValidationError("mvcc.RaftCompare.Validate", "compare revision cannot be negative")
		}
	case RaftCompareValue:
		// Value-based comparisons
		if c.Value == nil {
			// Allow nil for "key does not exist" checks
			return nil
		}
		switch c.Value.(type) {
		case []byte, string:
			// Valid types
		default:
			return api.NewUnsupportedValueTypeError("mvcc.RaftCompare.Validate", fmt.Sprintf("%T", c.Value))
		}
	default:
		return api.NewUnknownCompareTargetError("mvcc.RaftCompare.Validate", int(c.Target))
	}

	switch c.Result {
	case RaftCompareEqual, RaftCompareNotEqual, RaftCompareGreater, RaftCompareLess, RaftCompareGreaterEqual, RaftCompareLessEqual:
		// Valid
	default:
		return api.NewUnknownCompareResultError("mvcc.RaftCompare.Validate", int(c.Result))
	}

	return nil
}
