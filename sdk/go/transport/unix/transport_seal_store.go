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

package unix

import (
	"context"

	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertions.
var (
	_ transport.SealStoreClient    = (*Transport)(nil)
	_ transport.SealStoreEndpoints = sealStoreEndpoints{}
)

// sealStoreEndpoints is the gRPC wire half of the shared remote
// SealStoreClient: one SealStore* RPC per call, each gated server-side on
// seal:store_manage.
type sealStoreEndpoints struct {
	t *Transport
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

// PutSealStoreSecret calls SealStorePut.
func (e sealStoreEndpoints) PutSealStoreSecret(ctx context.Context, name string, secret []byte) error {
	if e.t.closed.Load() {
		return &transport.ClosedError{}
	}
	if err := e.t.requireTLS(transport.SealStoreSecret(true)); err != nil {
		return err
	}
	if _, err := e.t.client.SealStorePut(ctx, &pb.SealStorePutRequest{Name: name, Secret: secret}); err != nil {
		return mapGRPCError("SealStorePut", err)
	}
	return nil
}

// GetSealStoreSecret calls SealStoreGet.
func (e sealStoreEndpoints) GetSealStoreSecret(ctx context.Context, name string) ([]byte, error) {
	if e.t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if err := e.t.requireTLS(transport.SealStoreSecret(true)); err != nil {
		return nil, err
	}
	resp, err := e.t.client.SealStoreGet(ctx, &pb.SealStoreNameRequest{Name: name})
	if err != nil {
		return nil, mapGRPCError("SealStoreGet", err)
	}
	return resp.GetSecret(), nil
}

// DeleteSealStoreSecret calls SealStoreDelete.
func (e sealStoreEndpoints) DeleteSealStoreSecret(ctx context.Context, name string) error {
	if e.t.closed.Load() {
		return &transport.ClosedError{}
	}
	if _, err := e.t.client.SealStoreDelete(ctx, &pb.SealStoreNameRequest{Name: name}); err != nil {
		return mapGRPCError("SealStoreDelete", err)
	}
	return nil
}

// ListSealStoreSecrets calls SealStoreList.
func (e sealStoreEndpoints) ListSealStoreSecrets(ctx context.Context) ([]string, error) {
	if e.t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := e.t.client.SealStoreList(ctx, &emptypb.Empty{})
	if err != nil {
		return nil, mapGRPCError("SealStoreList", err)
	}
	return resp.GetNames(), nil
}

// ResealSealStoreSecret calls SealStoreReseal.
func (e sealStoreEndpoints) ResealSealStoreSecret(ctx context.Context, name string) error {
	if e.t.closed.Load() {
		return &transport.ClosedError{}
	}
	if _, err := e.t.client.SealStoreReseal(ctx, &pb.SealStoreNameRequest{Name: name}); err != nil {
		return mapGRPCError("SealStoreReseal", err)
	}
	return nil
}

// GetSealStoreStatus calls SealStoreStatus.
func (e sealStoreEndpoints) GetSealStoreStatus(ctx context.Context) (*transport.SealStoreStatusResponse, error) {
	if e.t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := e.t.client.SealStoreStatus(ctx, &emptypb.Empty{})
	if err != nil {
		return nil, mapGRPCError("SealStoreStatus", err)
	}
	return &transport.SealStoreStatusResponse{
		Available:   resp.GetAvailable(),
		SealerID:    resp.GetSealerId(),
		SecretCount: int(resp.GetSecretCount()),
		SecretNames: resp.GetSecretNames(),
	}, nil
}
