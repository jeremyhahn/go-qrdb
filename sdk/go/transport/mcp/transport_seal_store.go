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

package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.SealStoreClient = (*Transport)(nil)

// sealStoreEndpoints serves transport.SealStoreEndpoints over the
// qrdb_seal_store_* MCP tools.
type sealStoreEndpoints struct {
	t *Transport
}

var _ transport.SealStoreEndpoints = sealStoreEndpoints{}

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

// PutSealStoreSecret calls qrdb_seal_store_put.
func (e sealStoreEndpoints) PutSealStoreSecret(ctx context.Context, name string, secret []byte) error {
	if e.t.closed.Load() {
		return &transport.ClosedError{}
	}
	ctx, err := e.t.guardSecret(ctx, transport.SealStoreSecret(true))
	if err != nil {
		return err
	}
	_, err = e.t.callTool(ctx, "qrdb_seal_store_put", map[string]any{
		"name":   name,
		"secret": base64.StdEncoding.EncodeToString(secret),
	})
	return err
}

// GetSealStoreSecret calls qrdb_seal_store_get.
func (e sealStoreEndpoints) GetSealStoreSecret(ctx context.Context, name string) ([]byte, error) {
	if e.t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	ctx, err := e.t.guardSecret(ctx, transport.SealStoreSecret(true))
	if err != nil {
		return nil, err
	}
	text, err := e.t.callTool(ctx, "qrdb_seal_store_get", map[string]any{"name": name})
	if err != nil {
		return nil, err
	}
	var raw struct {
		Secret string `json:"secret"`
	}
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "SealStoreGet", Err: &jsonDecodeError{Err: jsonErr}}
	}
	secret, decErr := base64.StdEncoding.DecodeString(raw.Secret)
	if decErr != nil {
		return nil, &transport.RequestError{Method: "SealStoreGet", Err: &jsonDecodeError{Err: decErr}}
	}
	return secret, nil
}

// DeleteSealStoreSecret calls qrdb_seal_store_delete.
func (e sealStoreEndpoints) DeleteSealStoreSecret(ctx context.Context, name string) error {
	if e.t.closed.Load() {
		return &transport.ClosedError{}
	}
	_, err := e.t.callTool(ctx, "qrdb_seal_store_delete", map[string]any{"name": name})
	return err
}

// ListSealStoreSecrets calls qrdb_seal_store_list.
func (e sealStoreEndpoints) ListSealStoreSecrets(ctx context.Context) ([]string, error) {
	if e.t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	text, err := e.t.callTool(ctx, "qrdb_seal_store_list", nil)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Names []string `json:"names"`
	}
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "SealStoreList", Err: &jsonDecodeError{Err: jsonErr}}
	}
	return raw.Names, nil
}

// ResealSealStoreSecret calls qrdb_seal_store_reseal.
func (e sealStoreEndpoints) ResealSealStoreSecret(ctx context.Context, name string) error {
	if e.t.closed.Load() {
		return &transport.ClosedError{}
	}
	_, err := e.t.callTool(ctx, "qrdb_seal_store_reseal", map[string]any{"name": name})
	return err
}

// GetSealStoreStatus calls qrdb_seal_store_status.
func (e sealStoreEndpoints) GetSealStoreStatus(ctx context.Context) (*transport.SealStoreStatusResponse, error) {
	if e.t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	text, err := e.t.callTool(ctx, "qrdb_seal_store_status", nil)
	if err != nil {
		return nil, err
	}
	var st transport.SealStoreStatusResponse
	if jsonErr := json.Unmarshal([]byte(text), &st); jsonErr != nil {
		return nil, &transport.RequestError{Method: "SealStoreStatus", Err: &jsonDecodeError{Err: jsonErr}}
	}
	return &st, nil
}
