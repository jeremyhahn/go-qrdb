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

package sdk

import (
	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// ---------------------------------------------------------------------------
// At-rest filesystem encryption
// ---------------------------------------------------------------------------

// AtRestEncryptor wraps a store's filesys.FS with barrier-driven
// AES-256-GCM encryption. A nil or no-op Encryptor preserves plaintext
// operation for tests and non-encrypted deployments.
//
// Obtain one via NewAtRestEncryptor(barrier) and pass it to any store that
// accepts an encryptor option (e.g. vectorstorage.WithEncryptor,
// timeseries.WithEncryptor, or PebbleKVStore options).
type AtRestEncryptor = atrest.Encryptor

// NewAtRestEncryptor returns an Encryptor that uses barrier as the
// key-encryption-key. A nil barrier produces a valid, no-op Encryptor.
func NewAtRestEncryptor(barrier *seal.StorageBarrier) *AtRestEncryptor {
	return atrest.New(barrier)
}
