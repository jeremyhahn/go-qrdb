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
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestMCP_UpdateDatabaseBarrier_NilBarrierIsRefused pins that a request with
// no barrier configuration is refused before the call: the tool reads an
// absent "enabled" as false, so sending it would disable the barrier.
func TestMCP_UpdateDatabaseBarrier_NilBarrierIsRefused(t *testing.T) {
	tr, cleanup := connectStubInProcess(t, map[string]string{"qrdb_database_update_barrier": `{"name":"app"}`})
	defer cleanup()
	_, err := tr.UpdateDatabaseBarrier(context.Background(), transport.UpdateDatabaseBarrierRequest{Name: "app"})
	var nre *nilRequestError
	if !errors.As(err, &nre) {
		t.Fatalf("UpdateDatabaseBarrier with no barrier = %v, want a nilRequestError", err)
	}
}
