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

package clusterkeys

import (
	"encoding/json"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// keyPayload is the JSON wire format for the system-managed delivery plaintext.
// It mirrors seal.ImportedKey field-for-field so the receiver can unmarshal it
// directly into the seal import seam. The self-managed mode does not use this
// codec — its HPKE plaintext is the already-sealed root-key blob verbatim.
//
// The field names are deliberately snake_case JSON to decouple the on-wire
// representation from the Go type names and to allow independent versioning.
type keyPayload struct {
	RootKey     []byte   `json:"root_key"`
	Salt        []byte   `json:"salt"`
	Epoch       uint64   `json:"epoch"`
	KnownEpochs []uint64 `json:"known_epochs"`
}

// marshalKeyPayload encodes an ImportedKey as JSON for HPKE-sealing by the
// sender. The returned bytes are the HPKE plaintext for system-managed mode.
// The caller is responsible for wiping the returned slice after use.
func marshalKeyPayload(k *seal.ImportedKey) ([]byte, error) {
	return json.Marshal(keyPayload{
		RootKey:     k.RootKey,
		Salt:        k.Salt,
		Epoch:       k.Epoch,
		KnownEpochs: k.KnownEpochs,
	})
}

// unmarshalKeyPayload decodes the JSON plaintext produced by marshalKeyPayload
// back into a seal.ImportedKey for BarrierRegistry.ImportTenant.
func unmarshalKeyPayload(b []byte) (*seal.ImportedKey, error) {
	var p keyPayload
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, err
	}
	return &seal.ImportedKey{
		RootKey:     p.RootKey,
		Salt:        p.Salt,
		Epoch:       p.Epoch,
		KnownEpochs: p.KnownEpochs,
	}, nil
}
