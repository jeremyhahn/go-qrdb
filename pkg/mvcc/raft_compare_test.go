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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRaftCompare_String(t *testing.T) {
	tests := []struct {
		name string
		cmp  *RaftCompare
	}{
		{
			name: "version equal",
			cmp: &RaftCompare{
				Key:      []byte("mykey"),
				Target:   RaftCompareVersion,
				Result:   RaftCompareEqual,
				Revision: 5,
			},
		},
		{
			name: "mod not equal",
			cmp: &RaftCompare{
				Key:      []byte("test"),
				Target:   RaftCompareMod,
				Result:   RaftCompareNotEqual,
				Revision: 100,
			},
		},
		{
			name: "create greater",
			cmp: &RaftCompare{
				Key:      []byte("key"),
				Target:   RaftCompareCreate,
				Result:   RaftCompareGreater,
				Revision: 50,
			},
		},
		{
			name: "value equal",
			cmp: &RaftCompare{
				Key:    []byte("val"),
				Target: RaftCompareValue,
				Result: RaftCompareEqual,
				Value:  []byte("expected"),
			},
		},
		{
			name: "lease less",
			cmp: &RaftCompare{
				Key:      []byte("lease_key"),
				Target:   RaftCompareLease,
				Result:   RaftCompareLess,
				Revision: 42,
			},
		},
		{
			name: "greater equal",
			cmp: &RaftCompare{
				Key:      []byte("key"),
				Target:   RaftCompareVersion,
				Result:   RaftCompareGreaterEqual,
				Revision: 10,
			},
		},
		{
			name: "less equal",
			cmp: &RaftCompare{
				Key:      []byte("key"),
				Target:   RaftCompareVersion,
				Result:   RaftCompareLessEqual,
				Revision: 20,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.cmp.String()
			assert.NotEmpty(t, result)
		})
	}
}

func TestRaftCompare_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cmp     *RaftCompare
		wantErr bool
	}{
		{
			name: "valid version compare",
			cmp: &RaftCompare{
				Key:      []byte("key"),
				Target:   RaftCompareVersion,
				Result:   RaftCompareEqual,
				Revision: 5,
			},
			wantErr: false,
		},
		{
			name: "valid value compare",
			cmp: &RaftCompare{
				Key:    []byte("key"),
				Target: RaftCompareValue,
				Result: RaftCompareEqual,
				Value:  []byte("value"),
			},
			wantErr: false,
		},
		{
			name: "empty key",
			cmp: &RaftCompare{
				Key:      []byte{},
				Target:   RaftCompareVersion,
				Result:   RaftCompareEqual,
				Revision: 1,
			},
			wantErr: true,
		},
		{
			name: "negative revision",
			cmp: &RaftCompare{
				Key:      []byte("key"),
				Target:   RaftCompareVersion,
				Result:   RaftCompareEqual,
				Revision: -1,
			},
			wantErr: true,
		},
		{
			name: "invalid target",
			cmp: &RaftCompare{
				Key:      []byte("key"),
				Target:   RaftCompareTarget(999),
				Result:   RaftCompareEqual,
				Revision: 1,
			},
			wantErr: true,
		},
		{
			name: "invalid result",
			cmp: &RaftCompare{
				Key:      []byte("key"),
				Target:   RaftCompareVersion,
				Result:   RaftCompareResult(999),
				Revision: 1,
			},
			wantErr: true,
		},
		{
			name: "nil value allowed",
			cmp: &RaftCompare{
				Key:    []byte("key"),
				Target: RaftCompareValue,
				Result: RaftCompareEqual,
				Value:  nil,
			},
			wantErr: false,
		},
		{
			name: "invalid value type",
			cmp: &RaftCompare{
				Key:    []byte("key"),
				Target: RaftCompareValue,
				Result: RaftCompareEqual,
				Value:  123, // int not allowed
			},
			wantErr: true,
		},
		{
			name: "valid lease compare",
			cmp: &RaftCompare{
				Key:      []byte("key"),
				Target:   RaftCompareLease,
				Result:   RaftCompareEqual,
				Revision: 42,
			},
			wantErr: false,
		},
		{
			name: "valid create compare",
			cmp: &RaftCompare{
				Key:      []byte("key"),
				Target:   RaftCompareCreate,
				Result:   RaftCompareNotEqual,
				Revision: 10,
			},
			wantErr: false,
		},
		{
			name: "valid mod compare",
			cmp: &RaftCompare{
				Key:      []byte("key"),
				Target:   RaftCompareMod,
				Result:   RaftCompareGreater,
				Revision: 50,
			},
			wantErr: false,
		},
		{
			name: "string value allowed",
			cmp: &RaftCompare{
				Key:    []byte("key"),
				Target: RaftCompareValue,
				Result: RaftCompareEqual,
				Value:  "test-string",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cmp.Validate()
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestRaftCompare_EvaluateVersion(t *testing.T) {
	tests := []struct {
		name     string
		cmp      *RaftCompare
		kv       *RaftKeyValue
		expected bool
	}{
		{
			name: "version equal match",
			cmp: &RaftCompare{
				Key:      []byte("key"),
				Target:   RaftCompareVersion,
				Result:   RaftCompareEqual,
				Revision: 5,
			},
			kv:       &RaftKeyValue{Version: 5},
			expected: true,
		},
		{
			name: "version equal mismatch",
			cmp: &RaftCompare{
				Key:      []byte("key"),
				Target:   RaftCompareVersion,
				Result:   RaftCompareEqual,
				Revision: 5,
			},
			kv:       &RaftKeyValue{Version: 3},
			expected: false,
		},
		{
			name: "version not equal",
			cmp: &RaftCompare{
				Key:      []byte("key"),
				Target:   RaftCompareVersion,
				Result:   RaftCompareNotEqual,
				Revision: 5,
			},
			kv:       &RaftKeyValue{Version: 3},
			expected: true,
		},
		{
			name: "version greater",
			cmp: &RaftCompare{
				Key:      []byte("key"),
				Target:   RaftCompareVersion,
				Result:   RaftCompareGreater,
				Revision: 5,
			},
			kv:       &RaftKeyValue{Version: 10},
			expected: true,
		},
		{
			name: "version less",
			cmp: &RaftCompare{
				Key:      []byte("key"),
				Target:   RaftCompareVersion,
				Result:   RaftCompareLess,
				Revision: 10,
			},
			kv:       &RaftKeyValue{Version: 5},
			expected: true,
		},
		{
			name: "version greater or equal (greater)",
			cmp: &RaftCompare{
				Key:      []byte("key"),
				Target:   RaftCompareVersion,
				Result:   RaftCompareGreaterEqual,
				Revision: 5,
			},
			kv:       &RaftKeyValue{Version: 10},
			expected: true,
		},
		{
			name: "version greater or equal (equal)",
			cmp: &RaftCompare{
				Key:      []byte("key"),
				Target:   RaftCompareVersion,
				Result:   RaftCompareGreaterEqual,
				Revision: 5,
			},
			kv:       &RaftKeyValue{Version: 5},
			expected: true,
		},
		{
			name: "version less or equal (less)",
			cmp: &RaftCompare{
				Key:      []byte("key"),
				Target:   RaftCompareVersion,
				Result:   RaftCompareLessEqual,
				Revision: 10,
			},
			kv:       &RaftKeyValue{Version: 5},
			expected: true,
		},
		{
			name: "version less or equal (equal)",
			cmp: &RaftCompare{
				Key:      []byte("key"),
				Target:   RaftCompareVersion,
				Result:   RaftCompareLessEqual,
				Revision: 10,
			},
			kv:       &RaftKeyValue{Version: 10},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tt.cmp.evaluate(tt.kv)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRaftCompare_EvaluateModRevision(t *testing.T) {
	tests := []struct {
		name     string
		cmp      *RaftCompare
		kv       *RaftKeyValue
		expected bool
	}{
		{
			name: "mod equal",
			cmp: &RaftCompare{
				Key:      []byte("key"),
				Target:   RaftCompareMod,
				Result:   RaftCompareEqual,
				Revision: 100,
			},
			kv:       &RaftKeyValue{ModRevision: 100},
			expected: true,
		},
		{
			name: "mod not equal",
			cmp: &RaftCompare{
				Key:      []byte("key"),
				Target:   RaftCompareMod,
				Result:   RaftCompareNotEqual,
				Revision: 100,
			},
			kv:       &RaftKeyValue{ModRevision: 50},
			expected: true,
		},
		{
			name: "mod greater",
			cmp: &RaftCompare{
				Key:      []byte("key"),
				Target:   RaftCompareMod,
				Result:   RaftCompareGreater,
				Revision: 50,
			},
			kv:       &RaftKeyValue{ModRevision: 100},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tt.cmp.evaluate(tt.kv)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRaftCompare_EvaluateCreateRevision(t *testing.T) {
	tests := []struct {
		name     string
		cmp      *RaftCompare
		kv       *RaftKeyValue
		expected bool
	}{
		{
			name: "create equal",
			cmp: &RaftCompare{
				Key:      []byte("key"),
				Target:   RaftCompareCreate,
				Result:   RaftCompareEqual,
				Revision: 50,
			},
			kv:       &RaftKeyValue{CreateRevision: 50},
			expected: true,
		},
		{
			name: "create less",
			cmp: &RaftCompare{
				Key:      []byte("key"),
				Target:   RaftCompareCreate,
				Result:   RaftCompareLess,
				Revision: 100,
			},
			kv:       &RaftKeyValue{CreateRevision: 50},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tt.cmp.evaluate(tt.kv)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRaftCompare_EvaluateValue(t *testing.T) {
	tests := []struct {
		name     string
		cmp      *RaftCompare
		kv       *RaftKeyValue
		expected bool
	}{
		{
			name: "value equal bytes",
			cmp: &RaftCompare{
				Key:    []byte("key"),
				Target: RaftCompareValue,
				Result: RaftCompareEqual,
				Value:  []byte("expected"),
			},
			kv:       &RaftKeyValue{Value: []byte("expected")},
			expected: true,
		},
		{
			name: "value equal string",
			cmp: &RaftCompare{
				Key:    []byte("key"),
				Target: RaftCompareValue,
				Result: RaftCompareEqual,
				Value:  "expected",
			},
			kv:       &RaftKeyValue{Value: []byte("expected")},
			expected: true,
		},
		{
			name: "value not equal",
			cmp: &RaftCompare{
				Key:    []byte("key"),
				Target: RaftCompareValue,
				Result: RaftCompareNotEqual,
				Value:  []byte("expected"),
			},
			kv:       &RaftKeyValue{Value: []byte("different")},
			expected: true,
		},
		{
			name: "value greater",
			cmp: &RaftCompare{
				Key:    []byte("key"),
				Target: RaftCompareValue,
				Result: RaftCompareGreater,
				Value:  []byte("aaa"),
			},
			kv:       &RaftKeyValue{Value: []byte("zzz")},
			expected: true,
		},
		{
			name: "value less",
			cmp: &RaftCompare{
				Key:    []byte("key"),
				Target: RaftCompareValue,
				Result: RaftCompareLess,
				Value:  []byte("zzz"),
			},
			kv:       &RaftKeyValue{Value: []byte("aaa")},
			expected: true,
		},
		{
			name: "value nil equal",
			cmp: &RaftCompare{
				Key:    []byte("key"),
				Target: RaftCompareValue,
				Result: RaftCompareEqual,
				Value:  nil,
			},
			kv:       &RaftKeyValue{Value: nil},
			expected: true,
		},
		{
			name: "value nil not equal",
			cmp: &RaftCompare{
				Key:    []byte("key"),
				Target: RaftCompareValue,
				Result: RaftCompareNotEqual,
				Value:  nil,
			},
			kv:       &RaftKeyValue{Value: []byte("something")},
			expected: true,
		},
		{
			name: "value greater equal (greater)",
			cmp: &RaftCompare{
				Key:    []byte("key"),
				Target: RaftCompareValue,
				Result: RaftCompareGreaterEqual,
				Value:  []byte("aaa"),
			},
			kv:       &RaftKeyValue{Value: []byte("zzz")},
			expected: true,
		},
		{
			name: "value greater equal (equal)",
			cmp: &RaftCompare{
				Key:    []byte("key"),
				Target: RaftCompareValue,
				Result: RaftCompareGreaterEqual,
				Value:  []byte("test"),
			},
			kv:       &RaftKeyValue{Value: []byte("test")},
			expected: true,
		},
		{
			name: "value less equal (less)",
			cmp: &RaftCompare{
				Key:    []byte("key"),
				Target: RaftCompareValue,
				Result: RaftCompareLessEqual,
				Value:  []byte("zzz"),
			},
			kv:       &RaftKeyValue{Value: []byte("aaa")},
			expected: true,
		},
		{
			name: "value less equal (equal)",
			cmp: &RaftCompare{
				Key:    []byte("key"),
				Target: RaftCompareValue,
				Result: RaftCompareLessEqual,
				Value:  []byte("test"),
			},
			kv:       &RaftKeyValue{Value: []byte("test")},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tt.cmp.evaluate(tt.kv)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRaftCompare_EvaluateLease(t *testing.T) {
	tests := []struct {
		name     string
		cmp      *RaftCompare
		kv       *RaftKeyValue
		expected bool
	}{
		{
			name: "lease equal",
			cmp: &RaftCompare{
				Key:      []byte("key"),
				Target:   RaftCompareLease,
				Result:   RaftCompareEqual,
				Revision: 42,
			},
			kv:       &RaftKeyValue{LeaseID: 42},
			expected: true,
		},
		{
			name: "lease not equal",
			cmp: &RaftCompare{
				Key:      []byte("key"),
				Target:   RaftCompareLease,
				Result:   RaftCompareNotEqual,
				Revision: 42,
			},
			kv:       &RaftKeyValue{LeaseID: 0},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tt.cmp.evaluate(tt.kv)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRaftCompare_EvaluateNilKeyValue(t *testing.T) {
	tests := []struct {
		name     string
		cmp      *RaftCompare
		expected bool
	}{
		{
			name: "version equal 0",
			cmp: &RaftCompare{
				Key:      []byte("missing"),
				Target:   RaftCompareVersion,
				Result:   RaftCompareEqual,
				Revision: 0,
			},
			expected: true,
		},
		{
			name: "mod equal 0",
			cmp: &RaftCompare{
				Key:      []byte("missing"),
				Target:   RaftCompareMod,
				Result:   RaftCompareEqual,
				Revision: 0,
			},
			expected: true,
		},
		{
			name: "create equal 0",
			cmp: &RaftCompare{
				Key:      []byte("missing"),
				Target:   RaftCompareCreate,
				Result:   RaftCompareEqual,
				Revision: 0,
			},
			expected: true,
		},
		{
			name: "value nil",
			cmp: &RaftCompare{
				Key:    []byte("missing"),
				Target: RaftCompareValue,
				Result: RaftCompareEqual,
				Value:  nil,
			},
			expected: true,
		},
		{
			name: "version not zero fails",
			cmp: &RaftCompare{
				Key:      []byte("missing"),
				Target:   RaftCompareVersion,
				Result:   RaftCompareEqual,
				Revision: 1,
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tt.cmp.evaluate(nil)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRaftCompare_EvaluateErrors(t *testing.T) {
	tests := []struct {
		name string
		cmp  *RaftCompare
		kv   *RaftKeyValue
	}{
		{
			name: "invalid target",
			cmp: &RaftCompare{
				Key:      []byte("key"),
				Target:   RaftCompareTarget(999),
				Result:   RaftCompareEqual,
				Revision: 1,
			},
			kv: &RaftKeyValue{},
		},
		{
			name: "invalid result for revision",
			cmp: &RaftCompare{
				Key:      []byte("key"),
				Target:   RaftCompareVersion,
				Result:   RaftCompareResult(999),
				Revision: 1,
			},
			kv: &RaftKeyValue{},
		},
		{
			name: "invalid value type",
			cmp: &RaftCompare{
				Key:    []byte("key"),
				Target: RaftCompareValue,
				Result: RaftCompareEqual,
				Value:  123, // Invalid type
			},
			kv: &RaftKeyValue{},
		},
		{
			name: "invalid result for value",
			cmp: &RaftCompare{
				Key:    []byte("key"),
				Target: RaftCompareValue,
				Result: RaftCompareResult(999),
				Value:  []byte("test"),
			},
			kv: &RaftKeyValue{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.cmp.evaluate(tt.kv)
			assert.Error(t, err)
		})
	}
}
