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
)

func TestRaftKeyValue_Clone(t *testing.T) {
	tests := []struct {
		name string
		kv   *RaftKeyValue
	}{
		{
			name: "nil RaftKeyValue",
			kv:   nil,
		},
		{
			name: "empty RaftKeyValue",
			kv:   &RaftKeyValue{},
		},
		{
			name: "complete RaftKeyValue",
			kv: &RaftKeyValue{
				Key:            []byte("test-key"),
				Value:          []byte("test-value"),
				CreateRevision: 100,
				ModRevision:    150,
				Version:        5,
				LeaseID:        42,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clone := tt.kv.Clone()

			if tt.kv == nil {
				assert.Nil(t, clone)
				return
			}

			// Check that clone is equal but not same reference
			assert.Equal(t, tt.kv, clone)
			if tt.kv.Key != nil {
				assert.NotSame(t, &tt.kv.Key, &clone.Key)
			}
			if tt.kv.Value != nil {
				assert.NotSame(t, &tt.kv.Value, &clone.Value)
			}

			// Modify clone and ensure original is unchanged
			if len(clone.Key) > 0 {
				clone.Key[0] = 'X'
				assert.NotEqual(t, tt.kv.Key, clone.Key)
			}
		})
	}
}
