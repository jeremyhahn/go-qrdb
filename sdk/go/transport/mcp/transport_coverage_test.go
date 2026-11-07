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

// The seal-store RPCs have no MCP tools: each refuses with a
// NotImplementedError rather than reaching the server.

package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestMCPSealStore_NotImplemented(t *testing.T) {
	tr := &Transport{config: &transport.Config{}}
	var nie *transport.NotImplementedError
	ctx := context.Background()

	if err := tr.SealStorePut(ctx, &transport.SealStorePutRequest{}); !errors.As(err, &nie) {
		t.Errorf("SealStorePut: %v", err)
	}
	if _, err := tr.SealStoreGet(ctx, &transport.SealStoreGetRequest{}); !errors.As(err, &nie) {
		t.Errorf("SealStoreGet: %v", err)
	}
	if err := tr.SealStoreDelete(ctx, &transport.SealStoreDeleteRequest{}); !errors.As(err, &nie) {
		t.Errorf("SealStoreDelete: %v", err)
	}
	if _, err := tr.SealStoreList(ctx); !errors.As(err, &nie) {
		t.Errorf("SealStoreList: %v", err)
	}
	if err := tr.SealStoreReseal(ctx, &transport.SealStoreResealRequest{}); !errors.As(err, &nie) {
		t.Errorf("SealStoreReseal: %v", err)
	}
	if _, err := tr.SealStoreStatus(ctx); !errors.As(err, &nie) {
		t.Errorf("SealStoreStatus: %v", err)
	}
}
