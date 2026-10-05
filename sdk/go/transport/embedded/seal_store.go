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

package embedded

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// SealStorePut stores a secret in the seal store.
func (t *Transport) SealStorePut(ctx context.Context, req *transport.SealStorePutRequest) error {
	if err := t.gate("SealStorePut"); err != nil {
		return err
	}
	if err := t.authorize(ctx, "SealStorePut"); err != nil {
		return err
	}
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if t.sealStoreSvc == nil {
		return &NilServiceError{Service: "SealStore"}
	}
	return t.sealStoreSvc.Put(ctx, req.Name, req.Secret)
}

// SealStoreGet retrieves a secret from the seal store.
func (t *Transport) SealStoreGet(ctx context.Context, req *transport.SealStoreGetRequest) (*transport.SealStoreGetResponse, error) {
	if err := t.gate("SealStoreGet"); err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "SealStoreGet"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.sealStoreSvc == nil {
		return nil, &NilServiceError{Service: "SealStore"}
	}
	secret, err := t.sealStoreSvc.Get(ctx, req.Name)
	if err != nil {
		return nil, err
	}
	return &transport.SealStoreGetResponse{
		Name:   req.Name,
		Secret: secret,
	}, nil
}

// SealStoreDelete deletes a secret from the seal store.
func (t *Transport) SealStoreDelete(ctx context.Context, req *transport.SealStoreDeleteRequest) error {
	if err := t.gate("SealStoreDelete"); err != nil {
		return err
	}
	if err := t.authorize(ctx, "SealStoreDelete"); err != nil {
		return err
	}
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if t.sealStoreSvc == nil {
		return &NilServiceError{Service: "SealStore"}
	}
	return t.sealStoreSvc.Delete(ctx, req.Name)
}

// SealStoreList lists all stored secret names.
func (t *Transport) SealStoreList(ctx context.Context) (*transport.SealStoreListResponse, error) {
	if err := t.gate("SealStoreList"); err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "SealStoreList"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.sealStoreSvc == nil {
		return nil, &NilServiceError{Service: "SealStore"}
	}
	names, err := t.sealStoreSvc.List(ctx)
	if err != nil {
		return nil, err
	}
	return &transport.SealStoreListResponse{
		Names: names,
	}, nil
}

// SealStoreReseal reseals a secret with the current sealing key.
func (t *Transport) SealStoreReseal(ctx context.Context, req *transport.SealStoreResealRequest) error {
	if err := t.gate("SealStoreReseal"); err != nil {
		return err
	}
	if err := t.authorize(ctx, "SealStoreReseal"); err != nil {
		return err
	}
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if t.sealStoreSvc == nil {
		return &NilServiceError{Service: "SealStore"}
	}
	return t.sealStoreSvc.Reseal(ctx, req.Name)
}

// SealStoreStatus returns the seal store status.
func (t *Transport) SealStoreStatus(ctx context.Context) (*transport.SealStoreStatusResponse, error) {
	if err := t.gate("SealStoreStatus"); err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "SealStoreStatus"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.sealStoreSvc == nil {
		return nil, &NilServiceError{Service: "SealStore"}
	}
	status, err := t.sealStoreSvc.Status(ctx)
	if err != nil {
		return nil, err
	}
	return &transport.SealStoreStatusResponse{
		Available:   status.Available,
		SealerID:    string(status.Strategy),
		SecretCount: status.SecretCount,
		SecretNames: status.SecretNames,
	}, nil
}

// WithSealStore injects a SealStore into the transport.
func WithSealStore(svc *seal.SealStore) Option {
	return func(t *Transport) {
		t.sealStoreSvc = svc
	}
}
