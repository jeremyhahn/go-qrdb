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

package rest

import (
	"context"
	"net/url"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertions.
var (
	_ transport.SealStoreClient    = (*Transport)(nil)
	_ transport.SealStoreEndpoints = sealStoreEndpoints{}
)

// sealStoreEndpoints is the REST wire half of the shared remote
// SealStoreClient: one /v1/seal-store route per call, each gated
// server-side on seal:store_manage.
type sealStoreEndpoints struct {
	t *Transport
}

// sealStoreSecretWire is the secret body of the put and get routes; the
// secret travels base64-encoded ([]byte in JSON).
type sealStoreSecretWire struct {
	Name   string `json:"name,omitempty"`
	Secret []byte `json:"secret"`
}

func sealStoreSecretPath(name string) string {
	return "/v1/seal-store/secrets/" + url.PathEscape(name)
}

func (t *Transport) sealStore() transport.SealStoreClient {
	return transport.NewRemoteSealStoreClient(sealStoreEndpoints{t: t})
}

// SealStorePut stores a secret in the node's seal store.
func (t *Transport) SealStorePut(ctx context.Context, req *transport.SealStorePutRequest) error {
	return t.sealStore().SealStorePut(ctx, req)
}

// SealStoreGet retrieves a secret from the node's seal store.
func (t *Transport) SealStoreGet(ctx context.Context, req *transport.SealStoreGetRequest) (*transport.SealStoreGetResponse, error) {
	return t.sealStore().SealStoreGet(ctx, req)
}

// SealStoreDelete deletes a secret from the node's seal store.
func (t *Transport) SealStoreDelete(ctx context.Context, req *transport.SealStoreDeleteRequest) error {
	return t.sealStore().SealStoreDelete(ctx, req)
}

// SealStoreList lists the names in the node's seal store.
func (t *Transport) SealStoreList(ctx context.Context) (*transport.SealStoreListResponse, error) {
	return t.sealStore().SealStoreList(ctx)
}

// SealStoreReseal reseals a secret under the current barrier key.
func (t *Transport) SealStoreReseal(ctx context.Context, req *transport.SealStoreResealRequest) error {
	return t.sealStore().SealStoreReseal(ctx, req)
}

// SealStoreStatus returns the node seal store's status.
func (t *Transport) SealStoreStatus(ctx context.Context) (*transport.SealStoreStatusResponse, error) {
	return t.sealStore().SealStoreStatus(ctx)
}

// PutSealStoreSecret calls POST /v1/seal-store/secrets/{name}.
func (e sealStoreEndpoints) PutSealStoreSecret(ctx context.Context, name string, secret []byte) error {
	if e.t.closed.Load() {
		return &transport.ClosedError{}
	}
	return e.t.postJSONNoContent(ctx, "SealStorePut", sealStoreSecretPath(name), sealStoreSecretWire{Secret: secret})
}

// GetSealStoreSecret calls GET /v1/seal-store/secrets/{name}.
func (e sealStoreEndpoints) GetSealStoreSecret(ctx context.Context, name string) ([]byte, error) {
	if e.t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	var resp sealStoreSecretWire
	if err := e.t.getJSONDecode(ctx, "SealStoreGet", sealStoreSecretPath(name), &resp); err != nil {
		return nil, err
	}
	return resp.Secret, nil
}

// DeleteSealStoreSecret calls DELETE /v1/seal-store/secrets/{name}.
func (e sealStoreEndpoints) DeleteSealStoreSecret(ctx context.Context, name string) error {
	if e.t.closed.Load() {
		return &transport.ClosedError{}
	}
	return e.t.deleteJSONDecode(ctx, "SealStoreDelete", sealStoreSecretPath(name), nil)
}

// ListSealStoreSecrets calls GET /v1/seal-store/secrets.
func (e sealStoreEndpoints) ListSealStoreSecrets(ctx context.Context) ([]string, error) {
	if e.t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	var resp struct {
		Names []string `json:"names"`
	}
	if err := e.t.getJSONDecode(ctx, "SealStoreList", "/v1/seal-store/secrets", &resp); err != nil {
		return nil, err
	}
	return resp.Names, nil
}

// ResealSealStoreSecret calls POST /v1/seal-store/secrets/{name}/reseal.
func (e sealStoreEndpoints) ResealSealStoreSecret(ctx context.Context, name string) error {
	if e.t.closed.Load() {
		return &transport.ClosedError{}
	}
	return e.t.postJSONNoContent(ctx, "SealStoreReseal", sealStoreSecretPath(name)+"/reseal", struct{}{})
}

// GetSealStoreStatus calls GET /v1/seal-store/status.
func (e sealStoreEndpoints) GetSealStoreStatus(ctx context.Context) (*transport.SealStoreStatusResponse, error) {
	if e.t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	var resp transport.SealStoreStatusResponse
	if err := e.t.getJSONDecode(ctx, "SealStoreStatus", "/v1/seal-store/status", &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
