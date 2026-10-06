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

package trustanchor

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// slowGetStore widens a Get-then-write race by sleeping between the read its
// caller performs and the value it returns, the way a replicated store's
// latency does. It is used to drive KVRegistry.Add's optimistic-concurrency
// loop under real contention instead of relying on goroutine scheduling
// alone to interleave reads and writes.
type slowGetStore struct {
	*kvstore.MemoryKVStore
	delay time.Duration
}

func (s *slowGetStore) Get(ctx context.Context, key string) ([]byte, error) {
	v, err := s.MemoryKVStore.Get(ctx, key)
	time.Sleep(s.delay)
	return v, err
}

// TestKVRegistry_ConcurrentAddNeverExceedsTheCap is the security review's
// concurrent-Add probe (TestSecReviewProbe_ConcurrentAddExceedsCap), turned
// into a permanent regression: 4*MaxAnchorsPerTenant goroutines each adding
// one distinct anchor to the same tenant, concurrently, must never leave
// more than MaxAnchorsPerTenant registered.
func TestKVRegistry_ConcurrentAddNeverExceedsTheCap(t *testing.T) {
	var bundles [][]byte
	for i := 0; i < 4*MaxAnchorsPerTenant; i++ {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		bundles = append(bundles, selfSignedCA(t, "c", &k.PublicKey, k, x509.ECDSAWithSHA256, nil))
	}

	store := &slowGetStore{MemoryKVStore: kvstore.NewMemoryKVStore(), delay: time.Millisecond}
	r := NewKVRegistry(store)
	ctx := context.Background()

	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, b := range bundles {
		wg.Add(1)
		go func(b []byte) {
			defer wg.Done()
			<-start
			_, _ = r.Add(ctx, "acme", b)
		}(b)
	}
	close(start)
	wg.Wait()

	list, err := r.List(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) > MaxAnchorsPerTenant {
		t.Fatalf("CAP EXCEEDED: %d anchors registered, want at most %d", len(list), MaxAnchorsPerTenant)
	}
}

// casFailNthStore fails the Nth call to CompareAndSwap (1-indexed) across
// every key, simulating a transaction-plane failure partway through an
// Add's atomic commit.
type casFailNthStore struct {
	*kvstore.MemoryKVStore
	n     int32
	count int32
}

func (s *casFailNthStore) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	s.count++
	if s.count == s.n {
		return false, errors.New("injected compare-and-swap failure")
	}
	return s.MemoryKVStore.CompareAndSwap(ctx, key, expected, next)
}

// TestKVRegistry_AddAppliesNothingOnAPartialFailure is the security review's
// partial-bundle probe (TestSecReviewProbe_PartialBundleOnPutFailure), turned
// into a permanent regression over the new single-record design: a bundle
// of three anchors added one at a time, where the SECOND add's commit fails,
// must leave the first add's anchor the only one registered -- never a
// bundle with the second or third silently dropped, and never zero when the
// first genuinely committed.
func TestKVRegistry_AddAppliesNothingOnAPartialFailure(t *testing.T) {
	ctx := context.Background()
	var certs []*x509.Certificate
	for i := 0; i < 3; i++ {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		pemb := selfSignedCA(t, "p", &k.PublicKey, k, x509.ECDSAWithSHA256, nil)
		c, err := ParseCertificates(pemb)
		if err != nil {
			t.Fatal(err)
		}
		certs = append(certs, c[0])
	}

	// n=2: the first anchor's PutIfAbsent-path CompareAndSwap-free create
	// succeeds (Add's FIRST ever write for a tenant uses PutIfAbsent, not
	// CompareAndSwap), so arm the failure on the second CompareAndSwap call,
	// which is this second Add.
	store := &casFailNthStore{MemoryKVStore: kvstore.NewMemoryKVStore(), n: 1}
	r := NewKVRegistry(store)

	if _, err := r.Add(ctx, "acme", EncodePEM(certs[0])); err != nil {
		t.Fatalf("first Add: %v", err)
	}
	store.n = 1 // arm the very next CompareAndSwap call (the second anchor's).
	store.count = 0

	_, err := r.Add(ctx, "acme", EncodePEM(certs[1]))
	if err == nil {
		t.Fatal("Add over an injected CompareAndSwap failure = nil error, want one")
	}

	list, err := r.List(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Fingerprint != Fingerprint(certs[0]) {
		t.Fatalf("NON-ATOMIC: after the failed second Add, registered = %v, want exactly the first anchor", list)
	}
}
