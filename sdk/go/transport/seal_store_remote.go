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

package transport

import (
	"context"
	"errors"
	"unicode"
)

// SealStoreEndpoints is the wire surface a remote SealStoreClient needs: one
// call per seal-store operation, each gated server-side by
// permission.SealStoreManage (seal:store_manage). Each network transport
// implements it over its own protocol.
type SealStoreEndpoints interface {
	// PutSealStoreSecret stores secret under name.
	PutSealStoreSecret(ctx context.Context, name string, secret []byte) error
	// GetSealStoreSecret returns the secret stored under name.
	GetSealStoreSecret(ctx context.Context, name string) ([]byte, error)
	// DeleteSealStoreSecret deletes the secret stored under name.
	DeleteSealStoreSecret(ctx context.Context, name string) error
	// ListSealStoreSecrets returns the stored secret names.
	ListSealStoreSecrets(ctx context.Context) ([]string, error)
	// ResealSealStoreSecret reseals the secret stored under name.
	ResealSealStoreSecret(ctx context.Context, name string) error
	// GetSealStoreStatus returns the seal store status.
	GetSealStoreStatus(ctx context.Context) (*SealStoreStatusResponse, error)
}

// ErrSealStoreNilRequest is returned for a nil seal-store request.
var ErrSealStoreNilRequest = errors.New("transport: seal store request is nil")

// ErrSealStoreEmptyName is returned for a seal-store request with no name.
// The name is checked client-side so an empty key never reaches the wire.
var ErrSealStoreEmptyName = errors.New("transport: seal store secret name is empty")

// ErrSealStoreInvalidName is returned for a seal-store request whose name is
// not one opaque segment: "." or "..", or a name holding '/', '\\' or a
// control character. It is the client-side twin of the server's
// seal.ErrInvalidSecretName, checked so such a name never reaches the wire.
var ErrSealStoreInvalidName = errors.New(`transport: seal store secret name must not be "." or "..", and must not contain '/', '\' or control characters`)

// remoteSealStoreClient answers SealStoreClient through SealStoreEndpoints.
type remoteSealStoreClient struct {
	endpoints SealStoreEndpoints
}

// NewRemoteSealStoreClient returns a SealStoreClient that answers through
// the server's seal-store endpoints e.
func NewRemoteSealStoreClient(e SealStoreEndpoints) SealStoreClient {
	return &remoteSealStoreClient{endpoints: e}
}

func sealStoreName(name string) error {
	if name == "" {
		return ErrSealStoreEmptyName
	}
	if name == "." || name == ".." {
		return ErrSealStoreInvalidName
	}
	for _, r := range name {
		if r == '/' || r == '\\' || unicode.IsControl(r) {
			return ErrSealStoreInvalidName
		}
	}
	return nil
}

// SealStorePut stores req.Secret under req.Name.
func (c *remoteSealStoreClient) SealStorePut(ctx context.Context, req *SealStorePutRequest) error {
	if req == nil {
		return ErrSealStoreNilRequest
	}
	if err := sealStoreName(req.Name); err != nil {
		return err
	}
	return c.endpoints.PutSealStoreSecret(ctx, req.Name, req.Secret)
}

// SealStoreGet returns the secret stored under req.Name.
func (c *remoteSealStoreClient) SealStoreGet(ctx context.Context, req *SealStoreGetRequest) (*SealStoreGetResponse, error) {
	if req == nil {
		return nil, ErrSealStoreNilRequest
	}
	if err := sealStoreName(req.Name); err != nil {
		return nil, err
	}
	secret, err := c.endpoints.GetSealStoreSecret(ctx, req.Name)
	if err != nil {
		return nil, err
	}
	return &SealStoreGetResponse{Name: req.Name, Secret: secret}, nil
}

// SealStoreDelete deletes the secret stored under req.Name.
func (c *remoteSealStoreClient) SealStoreDelete(ctx context.Context, req *SealStoreDeleteRequest) error {
	if req == nil {
		return ErrSealStoreNilRequest
	}
	if err := sealStoreName(req.Name); err != nil {
		return err
	}
	return c.endpoints.DeleteSealStoreSecret(ctx, req.Name)
}

// SealStoreList returns the stored secret names.
func (c *remoteSealStoreClient) SealStoreList(ctx context.Context) (*SealStoreListResponse, error) {
	names, err := c.endpoints.ListSealStoreSecrets(ctx)
	if err != nil {
		return nil, err
	}
	if names == nil {
		names = []string{}
	}
	return &SealStoreListResponse{Names: names}, nil
}

// SealStoreReseal reseals the secret stored under req.Name.
func (c *remoteSealStoreClient) SealStoreReseal(ctx context.Context, req *SealStoreResealRequest) error {
	if req == nil {
		return ErrSealStoreNilRequest
	}
	if err := sealStoreName(req.Name); err != nil {
		return err
	}
	return c.endpoints.ResealSealStoreSecret(ctx, req.Name)
}

// SealStoreStatus returns the seal store status.
func (c *remoteSealStoreClient) SealStoreStatus(ctx context.Context) (*SealStoreStatusResponse, error) {
	return c.endpoints.GetSealStoreStatus(ctx)
}
