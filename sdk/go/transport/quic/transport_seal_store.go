// Copyright 2026 Jeremy Hahn
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

package quic

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// The seal-store verbs are embedded-only (ruling 1234,
// transport.EmbeddedOnlyOperations): QUIC refuses each with
// *transport.EmbeddedOnlyError and no network I/O. Seal and unseal stay on
// this transport (transport_barrier.go).
var _ transport.SealStoreClient = (*Transport)(nil)

// SealStorePut is embedded-only; it returns *transport.EmbeddedOnlyError.
func (t *Transport) SealStorePut(context.Context, *transport.SealStorePutRequest) error {
	return transport.RefuseEmbeddedOnly("quic", transport.OpSealStorePut)
}

// SealStoreGet is embedded-only; it returns *transport.EmbeddedOnlyError.
func (t *Transport) SealStoreGet(context.Context, *transport.SealStoreGetRequest) (*transport.SealStoreGetResponse, error) {
	return nil, transport.RefuseEmbeddedOnly("quic", transport.OpSealStoreGet)
}

// SealStoreDelete is embedded-only; it returns *transport.EmbeddedOnlyError.
func (t *Transport) SealStoreDelete(context.Context, *transport.SealStoreDeleteRequest) error {
	return transport.RefuseEmbeddedOnly("quic", transport.OpSealStoreDelete)
}

// SealStoreList is embedded-only; it returns *transport.EmbeddedOnlyError.
func (t *Transport) SealStoreList(context.Context) (*transport.SealStoreListResponse, error) {
	return nil, transport.RefuseEmbeddedOnly("quic", transport.OpSealStoreList)
}

// SealStoreReseal is embedded-only; it returns *transport.EmbeddedOnlyError.
func (t *Transport) SealStoreReseal(context.Context, *transport.SealStoreResealRequest) error {
	return transport.RefuseEmbeddedOnly("quic", transport.OpSealStoreReseal)
}

// SealStoreStatus is embedded-only; it returns *transport.EmbeddedOnlyError.
func (t *Transport) SealStoreStatus(context.Context) (*transport.SealStoreStatusResponse, error) {
	return nil, transport.RefuseEmbeddedOnly("quic", transport.OpSealStoreStatus)
}
