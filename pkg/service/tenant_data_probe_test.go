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

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kv"
)

// probeMVCC answers Range with a fixed count or error. Only Range is
// implemented; the probe calls nothing else, and the embedded nil interface
// makes any other call fail loudly.
type probeMVCC struct {
	MVCCKVServicer
	count int64
	err   error
}

func (m *probeMVCC) Range(context.Context, []byte, []byte, ...RangeOption) ([]*kv.KeyValue, int64, int64, error) {
	return nil, m.count, 0, m.err
}

// probeResolver resolves the fixed services a probe test wires.
type probeResolver struct {
	kv      KVServicer
	kvErr   error
	mvcc    MVCCKVServicer
	mvccErr error
}

func (r *probeResolver) KVService(string, string) (KVServicer, error)       { return r.kv, r.kvErr }
func (r *probeResolver) MVCCService(string, string) (MVCCKVServicer, error) { return r.mvcc, r.mvccErr }

func TestTenantDataProbe(t *testing.T) {
	withKey := newAFMockKV()
	withKey.store["user-key"] = []byte("v")
	rangeErr := errors.New("range timeout")
	kvErr := errors.New("leader unavailable")

	tests := []struct {
		name    string
		r       *probeResolver
		want    bool
		wantErr error
	}{
		// Relational tables live under reserved prefixes the KV list filters
		// out: the unfiltered MVCC count is what sees them.
		{"mvcc count sees relational-only data", &probeResolver{kv: newAFMockKV(), mvcc: &probeMVCC{count: 3}}, true, nil},
		{"mvcc count of zero is empty", &probeResolver{kv: withKey, mvcc: &probeMVCC{}}, false, nil},
		{"mvcc error fails closed", &probeResolver{kv: newAFMockKV(), mvcc: &probeMVCC{err: rangeErr}}, false, rangeErr},
		{"non-mvcc shard answers from the kv list", &probeResolver{kv: withKey, mvcc: &probeMVCC{err: &MVCCBackendRequiredError{}}}, true, nil},
		{"no mvcc service answers from the kv list", &probeResolver{kv: newAFMockKV()}, false, nil},
		{"mvcc resolve error answers from the kv list", &probeResolver{kv: withKey, mvccErr: errors.New("no mvcc")}, true, nil},
		{"kv resolve error fails closed", &probeResolver{kvErr: kvErr}, false, kvErr},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NewTenantDataProbe(tc.r).TenantHasData(context.Background(), "acme")
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("hasData = %v, want %v", got, tc.want)
			}
		})
	}
}
