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

package tenant

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// policyMapBackend is a linearizable clusterPolicyBackend: every
// operation holds one mutex, as the replicated store's atomic primitives
// do. swapErr, when set, fails CompareAndSwap without changing the value.
type policyMapBackend struct {
	mu      sync.Mutex
	data    map[string][]byte
	swapErr error
}

func newPolicyMapBackend() *policyMapBackend {
	return &policyMapBackend{data: map[string][]byte{}}
}

func (b *policyMapBackend) Get(_ context.Context, key string) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	v, ok := b.data[key]
	if !ok {
		return nil, seal.ErrSecretNotFound
	}
	return append([]byte(nil), v...), nil
}

func (b *policyMapBackend) PutIfAbsent(_ context.Context, key string, value []byte) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.data[key]; ok {
		return false, nil
	}
	b.data[key] = append([]byte(nil), value...)
	return true, nil
}

func (b *policyMapBackend) CompareAndSwap(_ context.Context, key string, expected, value []byte) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.swapErr != nil {
		return false, b.swapErr
	}
	if cur, ok := b.data[key]; !ok || !bytes.Equal(cur, expected) {
		return false, nil
	}
	b.data[key] = append([]byte(nil), value...)
	return true, nil
}

func (b *policyMapBackend) floor(t *testing.T) ClusterIsolationPolicy {
	t.Helper()
	got, err := getRawClusterIsolationPolicy(context.Background(), b)
	if err != nil {
		t.Fatalf("getRawClusterIsolationPolicy: %v", err)
	}
	return got
}

// TestSetClusterIsolationPolicy_TightenRacesNeverLeaveFloorLoose races a
// strict setter against operator-access writers starting from both the
// unset and the operator-access floor: once a strict setter reports
// success, the stored floor is strict.
func TestSetClusterIsolationPolicy_TightenRacesNeverLeaveFloorLoose(t *testing.T) {
	ctx := context.Background()
	for _, start := range []ClusterIsolationPolicy{ClusterIsolationPolicyUnset, ClusterIsolationPolicyOperatorAccess} {
		for i := 0; i < 200; i++ {
			b := newPolicyMapBackend()
			if start != ClusterIsolationPolicyUnset {
				b.data[clusterIsolationPolicyKey] = []byte(start)
			}
			var wg sync.WaitGroup
			strictErrs := make([]error, 4)
			for w := 0; w < 4; w++ {
				wg.Add(2)
				go func() {
					defer wg.Done()
					_ = setClusterIsolationPolicy(ctx, b, ClusterIsolationPolicyOperatorAccess)
				}()
				go func(w int) {
					defer wg.Done()
					strictErrs[w] = setClusterIsolationPolicy(ctx, b, ClusterIsolationPolicyStrict)
				}(w)
			}
			wg.Wait()
			succeeded := false
			for _, err := range strictErrs {
				var changeErr *IsolationPolicyChangeError
				switch {
				case err == nil:
					succeeded = true
				case errors.As(err, &changeErr):
				default:
					t.Fatalf("start %q: strict setter error = %v, want nil or *IsolationPolicyChangeError", start, err)
				}
			}
			if succeeded {
				if got := b.floor(t); got != ClusterIsolationPolicyStrict {
					t.Fatalf("start %q: a strict setter reported success but the floor is %q", start, got)
				}
			}
		}
	}
}

// TestSetClusterIsolationPolicy_LostSwapRefused covers a swap that loses
// to a writer that changed the floor after the read: the setter reports
// *IsolationPolicyChangeError, never success.
func TestSetClusterIsolationPolicy_LostSwapRefused(t *testing.T) {
	b := newPolicyMapBackend()
	b.data[clusterIsolationPolicyKey] = []byte(ClusterIsolationPolicyOperatorAccess)
	lost := &lostSwapBackend{policyMapBackend: b}
	err := setClusterIsolationPolicy(context.Background(), lost, ClusterIsolationPolicyStrict)
	var changeErr *IsolationPolicyChangeError
	if !errors.As(err, &changeErr) {
		t.Fatalf("Set(strict) after a lost swap = %v, want *IsolationPolicyChangeError", err)
	}
}

// lostSwapBackend rewrites the stored floor to a foreign value between
// the setter's read and its swap.
type lostSwapBackend struct {
	*policyMapBackend
}

func (l *lostSwapBackend) CompareAndSwap(ctx context.Context, key string, expected, value []byte) (bool, error) {
	l.mu.Lock()
	l.data[key] = []byte("foreign")
	l.mu.Unlock()
	return l.policyMapBackend.CompareAndSwap(ctx, key, expected, value)
}

// TestSetClusterIsolationPolicy_SwapFailureKeepsOperatorAccess covers a
// store failure and a cancelled context at the swap: the setter reports
// the failure and the floor stays operator-access, never unset.
func TestSetClusterIsolationPolicy_SwapFailureKeepsOperatorAccess(t *testing.T) {
	for _, swapErr := range []error{errors.New("raft: proposal dropped"), context.Canceled} {
		b := newPolicyMapBackend()
		b.data[clusterIsolationPolicyKey] = []byte(ClusterIsolationPolicyOperatorAccess)
		b.swapErr = swapErr
		err := setClusterIsolationPolicy(context.Background(), b, ClusterIsolationPolicyStrict)
		var storeErr *StoreError
		if !errors.As(err, &storeErr) || !errors.Is(err, swapErr) {
			t.Fatalf("Set(strict) with swap error %v = %v, want *StoreError wrapping it", swapErr, err)
		}
		if got := b.floor(t); got != ClusterIsolationPolicyOperatorAccess {
			t.Fatalf("floor after a failed swap = %q, want operator-access", got)
		}
	}
}
