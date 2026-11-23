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

package quic

import (
	"context"
	"encoding/json"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

var _ transport.SealStoreClient = (*Transport)(nil)

// sealStoreEndpoints implements transport.SealStoreEndpoints over the QUIC
// sealstore.* methods.
type sealStoreEndpoints struct {
	t *Transport
}

type sealStoreNameWire struct {
	Name string `json:"name"`
}

type sealStorePutWire struct {
	Name   string `json:"name"`
	Secret []byte `json:"secret"`
}

func (t *Transport) sealStore() transport.SealStoreClient {
	return transport.NewRemoteSealStoreClient(sealStoreEndpoints{t: t})
}

// SealStorePut stores a secret in the node seal store.
func (t *Transport) SealStorePut(ctx context.Context, req *transport.SealStorePutRequest) error {
	return t.sealStore().SealStorePut(ctx, req)
}

// SealStoreGet returns a secret from the node seal store.
func (t *Transport) SealStoreGet(ctx context.Context, req *transport.SealStoreGetRequest) (*transport.SealStoreGetResponse, error) {
	return t.sealStore().SealStoreGet(ctx, req)
}

// SealStoreDelete deletes a secret from the node seal store.
func (t *Transport) SealStoreDelete(ctx context.Context, req *transport.SealStoreDeleteRequest) error {
	return t.sealStore().SealStoreDelete(ctx, req)
}

// SealStoreList lists the node seal store's secret names.
func (t *Transport) SealStoreList(ctx context.Context) (*transport.SealStoreListResponse, error) {
	return t.sealStore().SealStoreList(ctx)
}

// SealStoreReseal reseals a secret under the current barrier.
func (t *Transport) SealStoreReseal(ctx context.Context, req *transport.SealStoreResealRequest) error {
	return t.sealStore().SealStoreReseal(ctx, req)
}

// SealStoreStatus returns the node seal store status.
func (t *Transport) SealStoreStatus(ctx context.Context) (*transport.SealStoreStatusResponse, error) {
	return t.sealStore().SealStoreStatus(ctx)
}

// do sends one sealstore.<method> call with req as its JSON payload and
// decodes the answer into resp when resp is non-nil. A call error is
// returned as-is, so its wire status survives.
func (e sealStoreEndpoints) do(ctx context.Context, method string, req, resp any) error {
	if e.t.closed.Load() {
		return &transport.ClosedError{}
	}
	var payload []byte
	if req != nil {
		var err error
		if payload, err = json.Marshal(req); err != nil {
			return &transport.RequestError{Method: method, Err: err}
		}
	}
	data, err := e.t.call(ctx, "sealstore."+method, payload)
	if err != nil {
		return err
	}
	if resp == nil {
		return nil
	}
	if err := json.Unmarshal(data, resp); err != nil {
		return &transport.RequestError{Method: method, Err: &UnmarshalError{Err: err}}
	}
	return nil
}

func (e sealStoreEndpoints) PutSealStoreSecret(ctx context.Context, name string, secret []byte) error {
	if err := e.t.requireTLS(transport.SealStoreSecret(true)); err != nil {
		return err
	}
	return e.do(ctx, "Put", sealStorePutWire{Name: name, Secret: secret}, nil)
}

func (e sealStoreEndpoints) GetSealStoreSecret(ctx context.Context, name string) ([]byte, error) {
	if err := e.t.requireTLS(transport.SealStoreSecret(true)); err != nil {
		return nil, err
	}
	var resp transport.SealStoreGetResponse
	if err := e.do(ctx, "Get", sealStoreNameWire{Name: name}, &resp); err != nil {
		return nil, err
	}
	return resp.Secret, nil
}

func (e sealStoreEndpoints) DeleteSealStoreSecret(ctx context.Context, name string) error {
	return e.do(ctx, "Delete", sealStoreNameWire{Name: name}, nil)
}

func (e sealStoreEndpoints) ListSealStoreSecrets(ctx context.Context) ([]string, error) {
	var resp transport.SealStoreListResponse
	if err := e.do(ctx, "List", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Names, nil
}

func (e sealStoreEndpoints) ResealSealStoreSecret(ctx context.Context, name string) error {
	return e.do(ctx, "Reseal", sealStoreNameWire{Name: name}, nil)
}

func (e sealStoreEndpoints) GetSealStoreStatus(ctx context.Context) (*transport.SealStoreStatusResponse, error) {
	var resp transport.SealStoreStatusResponse
	if err := e.do(ctx, "Status", nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
