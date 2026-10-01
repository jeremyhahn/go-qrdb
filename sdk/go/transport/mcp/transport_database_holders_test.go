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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/dbholdertest"
)

// TestMCPInProcess_DatabaseHolders runs the database share-holder suite
// through the SDK MCP client against the real MCP tools over a real database
// service. The in-process session carries no certificate, so its claim is
// refused.
func TestMCPInProcess_DatabaseHolders(t *testing.T) {
	fx := dbholdertest.NewFixture(t)
	tr, cleanup := connectDatabaseInProcess(t, fx.Service)
	t.Cleanup(cleanup)
	dbholdertest.Run(t, tr, fx, dbholdertest.ClaimRefusedWithStatus(int(wire.CodeUnauthenticated)))
}
