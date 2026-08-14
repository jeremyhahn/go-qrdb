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

package cert

import (
	"context"
	"testing"
)

// ---------------------------------------------------------------------------
// getCAMaterial — auto-init branch (56.2%)
// ---------------------------------------------------------------------------

// TestGetCAMaterial_AutoInit verifies that getCAMaterial calls Initialize when
// the in-memory CA key has not yet been loaded (e.g. after a server restart
// where the barrier is unsealed but the key fields are nil). This exercises
// the `if key == nil || cert == nil` branch and the subsequent second-read path.
func TestGetCAMaterial_AutoInit(t *testing.T) {
	// Initialize a provider so the CA is persisted in the KV store.
	p, _, _ := initializeProvider(t)

	// Wipe the in-memory key/cert to simulate the post-restart state where
	// the barrier is unsealed but the CA material has not been loaded yet.
	p.mu.Lock()
	p.caKey = nil
	p.caCert = nil
	p.caCertPEM = nil
	p.mu.Unlock()

	// getCAMaterial should auto-initialize from the KV store.
	key, cert, err := p.getCAMaterial()
	if err != nil {
		t.Fatalf("getCAMaterial after clearing in-memory state: %v", err)
	}
	if len(key) == 0 {
		t.Fatal("expected non-empty CA key after auto-init")
	}
	if cert == nil {
		t.Fatal("expected non-nil CA cert after auto-init")
	}
}

// TestGetCAMaterial_AutoInit_InitializeError verifies that getCAMaterial
// propagates errors from Initialize when it is called on a provider whose
// barrier is sealed (triggering BarrierSealedError from within Initialize).
func TestGetCAMaterial_AutoInit_InitializeError(t *testing.T) {
	p, _, barrier := initializeProvider(t)

	// Wipe in-memory state.
	p.mu.Lock()
	p.caKey = nil
	p.caCert = nil
	p.caCertPEM = nil
	p.mu.Unlock()

	// Seal the barrier AFTER clearing in-memory state so getCAMaterial tries
	// to auto-initialize but Initialize returns BarrierSealedError.
	// Note: IsSealed() is checked first in getCAMaterial, so we need a way to
	// get past that check and into the nil-key branch. We achieve this by
	// sealing AFTER the initial IsSealed check would have passed (the check
	// uses the current barrier state, so just sealing it here means the first
	// IsSealed check returns true and we get the sealed error immediately).
	// That path is already covered. Instead exercise the Initialize error path
	// by providing a barrier that is unsealed for IsSealed but fails Encrypt.
	_ = barrier

	// Use a fresh provider with no persisted CA and an error store so
	// Initialize fails during the generate-new-CA path.
	p2 := NewDefaultProvider(&errorKVStore{err: errInjected}, newMockBarrier())

	// Wipe the in-memory key so getCAMaterial tries auto-init.
	p2.mu.Lock()
	p2.caKey = nil
	p2.caCert = nil
	p2.mu.Unlock()

	_, _, err := p2.getCAMaterial()
	if err == nil {
		t.Fatal("expected error from Initialize when KV store fails")
	}
}

// errInjected is a sentinel error used for injection.
var errInjected = stringError("injected error")

type stringError string

func (e stringError) Error() string { return string(e) }

// ---------------------------------------------------------------------------
// GenerateClientCert — auto-init via getCAMaterial (83.8%)
// ---------------------------------------------------------------------------

// TestGenerateClientCert_AutoInitViaGetCAMaterial exercises the path where
// GenerateClientCert triggers getCAMaterial which in turn calls Initialize
// because the in-memory CA key was cleared.
func TestGenerateClientCert_AutoInitViaGetCAMaterial(t *testing.T) {
	p, _, _ := initializeProvider(t)

	// Simulate a post-unseal restart: clear in-memory CA key.
	p.mu.Lock()
	p.caKey = nil
	p.caCert = nil
	p.caCertPEM = nil
	p.mu.Unlock()

	req := &GenerateRequest{Username: "auto-init-user", TenantID: "system"}
	bundle, err := p.GenerateClientCert(context.Background(), req)
	if err != nil {
		t.Fatalf("GenerateClientCert with auto-init: %v", err)
	}
	if bundle.Serial == "" {
		t.Fatal("expected non-empty serial after auto-init")
	}
}
