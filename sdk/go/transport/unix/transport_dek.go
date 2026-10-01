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

package unix

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/dekconv"
)

// Compile-time interface assertion.
var _ transport.DEKClient = (*Transport)(nil)

// DEKClient implementation: the QRDBService TenantDEK* RPCs over the Unix socket, issued
// through the dekconv.Caller the gRPC and Unix transports share. The server
// handlers live in pkg/api/grpc/service_dek.go.

// dek returns the shared RPC caller over this transport's client.
func (t *Transport) dek() dekconv.Caller {
	return dekconv.Caller{Client: t.client, MapError: mapGRPCError}
}

// TenantDEKStatus returns the caller's keyring versions.
func (t *Transport) TenantDEKStatus(ctx context.Context, req *transport.TenantDEKRequest) (*transport.TenantDEKStatusResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return t.dek().Status(ctx, req)
}

// TenantDEKRotate adds a key version and makes it current.
func (t *Transport) TenantDEKRotate(ctx context.Context, req *transport.TenantDEKRequest) (*transport.TenantDEKStatusResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return t.dek().Rotate(ctx, req)
}

// TenantDEKRetire destroys an old key version.
func (t *Transport) TenantDEKRetire(ctx context.Context, req *transport.TenantDEKRetireRequest) (*transport.TenantDEKStatusResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return t.dek().Retire(ctx, req)
}

// TenantDEKEncrypt seals plaintext under the current key version.
func (t *Transport) TenantDEKEncrypt(ctx context.Context, req *transport.TenantDEKEncryptRequest) (*transport.TenantDEKEncryptResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return t.dek().Encrypt(ctx, req)
}

// TenantDEKDecrypt opens a ciphertext sealed for the caller's tenant.
func (t *Transport) TenantDEKDecrypt(ctx context.Context, req *transport.TenantDEKDecryptRequest) (*transport.TenantDEKDecryptResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return t.dek().Decrypt(ctx, req)
}

// TenantDEKRewrap re-seals a ciphertext under the current key version.
func (t *Transport) TenantDEKRewrap(ctx context.Context, req *transport.TenantDEKRewrapRequest) (*transport.TenantDEKRewrapResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return t.dek().Rewrap(ctx, req)
}
