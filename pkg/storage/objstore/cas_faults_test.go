// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package objstore

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// putFailStore fails Put for every object once armed.
type putFailStore struct {
	*faultStore
	armed bool
}

func (p *putFailStore) Put(name string, data io.Reader) error {
	if p.armed {
		return errProvider
	}
	return p.faultStore.Put(name, data)
}

func TestCompareAndSwap_ProviderFaults(t *testing.T) {
	ctx := context.Background()
	short := []byte("k")
	long := patterned(directThreshold()+1, 10)
	for label, key := range map[string][]byte{"direct": short, "hashed": long} {
		t.Run(label+"/read fails", func(t *testing.T) {
			e, fs := newFaultEngine(t)
			require.NoError(t, e.Set(key, []byte("a")))
			name, _ := objectName(key)
			fs.getFails[name] = true
			swapped, err := e.CompareAndSwap(ctx, key, []byte("a"), []byte("b"))
			assert.ErrorIs(t, err, errProvider)
			assert.False(t, swapped)
		})
		t.Run(label+"/write fails", func(t *testing.T) {
			e, fs := newFaultEngine(t)
			require.NoError(t, e.Set(key, []byte("a")))
			pf := &putFailStore{faultStore: fs}
			e.store = pf
			pf.armed = true
			swapped, err := e.CompareAndSwap(ctx, key, []byte("a"), []byte("b"))
			assert.ErrorIs(t, err, errProvider)
			assert.False(t, swapped)
			pf.armed = false
			got, err := e.Get(key)
			require.NoError(t, err)
			assert.Equal(t, "a", string(got))
		})
		t.Run(label+"/swaps", func(t *testing.T) {
			e, _ := newFaultEngine(t)
			require.NoError(t, e.Set(key, []byte("a")))
			swapped, err := e.CompareAndSwap(ctx, key, []byte("a"), []byte("b"))
			require.NoError(t, err)
			assert.True(t, swapped)
			got, err := e.Get(key)
			require.NoError(t, err)
			assert.Equal(t, "b", string(got))
		})
	}
}
