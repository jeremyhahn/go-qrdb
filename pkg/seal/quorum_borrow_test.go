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

package seal

import (
	"context"
	"crypto/cipher"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/jeremyhahn/go-enclave"
	"github.com/jeremyhahn/go-quicraft/pkg/crypto/shamir"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// quorumToleranceBound is how long a completing quorum may take before the
// tolerance tests call it wedged. A completing round takes milliseconds; a
// deadlocked one never returns.
const quorumToleranceBound = 30 * time.Second

// keyAddrProvider is an AEADProvider that records the address of every key it
// is handed and then builds the real software AEAD over it. With inner set it
// also allocates, opens and destroys protected memory of its own before
// building, as a hardware-backed or KMS provider may.
type keyAddrProvider struct {
	inner *secure.Secret

	mu    sync.Mutex
	addrs []*byte
}

var _ AEADProvider = (*keyAddrProvider)(nil)

func (p *keyAddrProvider) NewAEAD(alg SymmetricAlgorithm, key []byte) (cipher.AEAD, error) {
	p.mu.Lock()
	p.addrs = append(p.addrs, unsafe.SliceData(key)) // #nosec G103 -- address comparison only; nothing is read through it.
	p.mu.Unlock()
	if p.inner != nil {
		scratch, err := secure.NewBuffer(len(key))
		if err != nil {
			return nil, err
		}
		defer scratch.Destroy()
		opened, err := p.inner.Open(context.Background())
		if err != nil {
			return nil, err
		}
		opened.Destroy()
	}
	return NewSoftwareAEADProvider().NewAEAD(alg, key)
}

// keys returns the addresses recorded so far.
func (p *keyAddrProvider) keys() []*byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]*byte(nil), p.addrs...)
}

// recordCombinedKey swaps the combineProtected seam for one that records the
// address of the locked pages each quorum reconstructs its key into, restoring
// the seam when t ends.
func recordCombinedKey(t *testing.T) func() *byte {
	t.Helper()
	var (
		mu   sync.Mutex
		addr *byte
	)
	prev := combineProtected
	combineProtected = func(shares []*shamir.ProtectedShare) (*enclave.MemoryEnclave, error) {
		enc, err := prev(shares)
		if err != nil {
			return nil, err
		}
		if err := enc.WithBytes(func(key []byte) error {
			mu.Lock()
			addr = unsafe.SliceData(key) // #nosec G103 -- address comparison only; nothing is read through it.
			mu.Unlock()
			return nil
		}); err != nil {
			return nil, err
		}
		return enc, nil
	}
	t.Cleanup(func() { combineProtected = prev })
	return func() *byte {
		mu.Lock()
		defer mu.Unlock()
		return addr
	}
}

// assertKeyIsTheEnclave fails t unless the provider was handed a key, and
// every key it was handed is the reconstructed enclave's own pages.
func assertKeyIsTheEnclave(t *testing.T, provider *keyAddrProvider, combined *byte) {
	t.Helper()
	if combined == nil {
		t.Fatal("no quorum reconstructed a key")
	}
	keys := provider.keys()
	if len(keys) == 0 {
		t.Fatal("the AEADProvider was never handed the reconstructed key")
	}
	for _, got := range keys {
		if got != combined {
			t.Fatalf("the AEADProvider received %p, not the reconstructed enclave at %p: "+
				"the unseal key crossed the heap on its way into the provider", got, combined)
		}
	}
}

// A completed unseal quorum hands the AEADProvider the enclave its key was
// reconstructed into, not a heap copy of it (go-qrdb#145 shape 1): the borrow
// on that enclave is held across the unwrap. A transient copy between combine
// and unwrap changes the address the provider sees and fails here.
func TestFinishRound_UnsealHandsTheProviderTheReconstructedEnclave(t *testing.T) {
	f := newHolderFixture(t, 2, 3)
	f.sealWith("h1", "h2")

	provider := &keyAddrProvider{}
	f.b.config.AEADProvider = provider
	combined := recordCombinedKey(t)

	defer secure.GuardBorrows(t.Fatalf)()
	if o := f.quorum(DomainUnseal, OpUnseal, "h1", "h3"); !o.Complete {
		t.Fatalf("unseal quorum incomplete: %+v", o)
	}
	if f.b.IsSealed() {
		t.Fatal("barrier still sealed after its unseal quorum")
	}
	assertKeyIsTheEnclave(t, provider, combined())
}

// The provider runs inside the reconstructed enclave's borrow, and a provider
// that allocates, opens and destroys protected memory of its own there must
// complete, with the armed guard silent, because none of it reenters that
// enclave. It runs under a bounded wait so a deadlock fails the test instead
// of hanging the package.
func TestFinishRound_ToleratesAProviderThatAllocatesProtectedMemory(t *testing.T) {
	inner, err := secure.NewSecret(context.Background(), []byte("the provider's own secret"))
	if err != nil {
		t.Fatalf("NewSecret: %v", err)
	}
	defer inner.Destroy()

	cases := map[string]func(t *testing.T, provider *keyAddrProvider) func() error{
		"unseal": func(t *testing.T, provider *keyAddrProvider) func() error {
			f := newHolderFixture(t, 2, 3)
			f.sealWith("h1", "h2")
			f.b.config.AEADProvider = provider
			return func() error {
				for _, h := range []string{"h1", "h3"} {
					if _, err := f.submit(DomainUnseal, OpUnseal, h); err != nil {
						return err
					}
				}
				return nil
			}
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			provider := &keyAddrProvider{inner: inner}
			run := setup(t, provider)

			stop := secure.GuardBorrows(t.Errorf)
			done := make(chan error, 1)
			go func() {
				// A guard violation Goexits this goroutine; the deferred
				// close still unblocks the wait, and stop reports it.
				defer close(done)
				done <- run()
			}()
			select {
			case err := <-done:
				stop()
				if err != nil {
					t.Fatalf("quorum: %v", err)
				}
			case <-time.After(quorumToleranceBound):
				stop()
				t.Fatalf("the quorum did not complete within %v: a provider using protected "+
					"memory inside the reconstructed key's borrow wedged it", quorumToleranceBound)
			}
			if len(provider.keys()) == 0 {
				t.Fatal("the provider was never called")
			}
		})
	}
}
