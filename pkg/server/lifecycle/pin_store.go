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

package lifecycle

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// sealPINKeyPrefix namespaces the PIN records inside the node-local seal
// store, beside the wrapped root key and the holder roster.
const sealPINKeyPrefix = "__pin/"

// sealPINStore adapts the node-local seal store to pin.Storage. The seal
// store is the plaintext store beneath the barrier, readable while sealed,
// which is where the PINs must live: the SO PIN authorizes operations the
// sealed node has to serve. The records are Argon2id (PBKDF2 under FIPS)
// hashes, never the PINs.
type sealPINStore struct {
	backend seal.StorageBackend
}

// Compile-time interface check.
var _ pin.Storage = sealPINStore{}

// Get returns the record under key, or pin.ErrNotFound when there is none.
func (s sealPINStore) Get(ctx context.Context, key string) ([]byte, error) {
	k := sealPINKeyPrefix + key
	ok, err := s.backend.Exists(ctx, k)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, pin.ErrNotFound
	}
	return s.backend.Get(ctx, k)
}

// Put writes the record under key.
func (s sealPINStore) Put(ctx context.Context, key string, value []byte) error {
	return s.backend.Put(ctx, sealPINKeyPrefix+key, value)
}

// NewSealStorePINBackend returns the node's software PIN backend, persisted
// in the node-local seal store. Every composer that does not inject a PIN
// backend of its own (a hardware one, say) wires this one, so the SO and
// User PINs have exactly one home per node: barrier initialization and the
// initialization ceremony both provision into it through pin.Provision.
func NewSealStorePINBackend(backend seal.StorageBackend) (*pin.SoftwareBackend, error) {
	if backend == nil {
		return nil, &NilSealStoreError{}
	}
	return pin.NewSoftwareBackend(sealPINStore{backend: backend}, pin.AutoDetectHashConfig())
}
