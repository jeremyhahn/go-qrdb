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

package pin

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// soKnownGoodStorageKeyPrefix prefixes the storage key of the node SO PIN
// lockout's known-good set (see auth.SOPINLockout): so-pin-known-good/{key}.
const soKnownGoodStorageKeyPrefix = "so-pin-known-good/"

var _ auth.SOPINKnownGoodStore = (*SoftwareBackend)(nil)

// LoadKnownGood implements auth.SOPINKnownGoodStore over the backend's
// storage, so the SO PIN lockout decorator (NewLockoutBackend) keeps a
// Security Officer's exemption from the aggregate cap across a restart. A
// backend with no storage persists nothing and loads an empty set.
func (b *SoftwareBackend) LoadKnownGood(key string) ([]string, error) {
	if b.store == nil {
		return nil, nil
	}
	data, err := b.store.Get(context.Background(), soKnownGoodStorageKeyPrefix+key)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var principals []string
	if err := json.Unmarshal(data, &principals); err != nil {
		return nil, &KnownGoodCodecError{Err: err}
	}
	return principals, nil
}

// SaveKnownGood implements auth.SOPINKnownGoodStore.
func (b *SoftwareBackend) SaveKnownGood(key string, principals []string) error {
	if b.store == nil {
		return nil
	}
	data, err := json.Marshal(principals)
	if err != nil {
		return &KnownGoodCodecError{Err: err}
	}
	return b.store.Put(context.Background(), soKnownGoodStorageKeyPrefix+key, data)
}

// KnownGoodCodecError is returned when a persisted known-good set cannot be
// encoded or decoded.
type KnownGoodCodecError struct {
	Err error
}

// Error returns a human-readable description.
func (e *KnownGoodCodecError) Error() string {
	return "pin: SO PIN known-good set: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *KnownGoodCodecError) Unwrap() error { return e.Err }
