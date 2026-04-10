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

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.SealStoreClient = (*Transport)(nil)

// SealStorePut stores a secret in the seal store. This method is not
// yet implemented for the MCP transport and requires corresponding MCP tool
// definitions.
func (t *Transport) SealStorePut(ctx context.Context, req *transport.SealStorePutRequest) error {
	return &transport.NotImplementedError{Method: "SealStorePut"}
}

// SealStoreGet retrieves a secret from the seal store. This method is
// not yet implemented for the MCP transport and requires corresponding MCP
// tool definitions.
func (t *Transport) SealStoreGet(ctx context.Context, req *transport.SealStoreGetRequest) (*transport.SealStoreGetResponse, error) {
	return nil, &transport.NotImplementedError{Method: "SealStoreGet"}
}

// SealStoreDelete deletes a secret from the seal store. This method is
// not yet implemented for the MCP transport and requires corresponding MCP
// tool definitions.
func (t *Transport) SealStoreDelete(ctx context.Context, req *transport.SealStoreDeleteRequest) error {
	return &transport.NotImplementedError{Method: "SealStoreDelete"}
}

// SealStoreList lists all stored secret names. This method is not yet
// implemented for the MCP transport and requires corresponding MCP tool
// definitions.
func (t *Transport) SealStoreList(ctx context.Context) (*transport.SealStoreListResponse, error) {
	return nil, &transport.NotImplementedError{Method: "SealStoreList"}
}

// SealStoreReseal reseals a secret with the current sealing key. This
// method is not yet implemented for the MCP transport and requires
// corresponding MCP tool definitions.
func (t *Transport) SealStoreReseal(ctx context.Context, req *transport.SealStoreResealRequest) error {
	return &transport.NotImplementedError{Method: "SealStoreReseal"}
}

// SealStoreStatus returns the seal store status. This method is not
// yet implemented for the MCP transport and requires corresponding MCP tool
// definitions.
func (t *Transport) SealStoreStatus(ctx context.Context) (*transport.SealStoreStatusResponse, error) {
	return nil, &transport.NotImplementedError{Method: "SealStoreStatus"}
}
