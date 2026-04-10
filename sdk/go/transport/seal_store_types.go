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

package transport

// SealStorePutRequest contains parameters for storing a secret.
type SealStorePutRequest struct {
	Name   string `json:"name"`
	Secret []byte `json:"secret"`
}

// SealStoreGetRequest contains parameters for retrieving a secret.
type SealStoreGetRequest struct {
	Name string `json:"name"`
}

// SealStoreGetResponse contains a retrieved secret.
type SealStoreGetResponse struct {
	Name   string `json:"name"`
	Secret []byte `json:"secret"`
}

// SealStoreDeleteRequest contains parameters for deleting a secret.
type SealStoreDeleteRequest struct {
	Name string `json:"name"`
}

// SealStoreListResponse contains a list of stored secret names.
type SealStoreListResponse struct {
	Names []string `json:"names"`
}

// SealStoreResealRequest contains parameters for resealing a secret.
type SealStoreResealRequest struct {
	Name string `json:"name"`
}

// SealStoreStatusResponse contains the seal store status.
type SealStoreStatusResponse struct {
	Available   bool     `json:"available"`
	SealerID    string   `json:"sealer_id,omitempty"`
	SecretCount int      `json:"secret_count"`
	SecretNames []string `json:"secret_names,omitempty"`
}
