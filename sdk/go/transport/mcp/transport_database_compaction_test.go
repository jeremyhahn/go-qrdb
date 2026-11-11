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
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestMCP_Database_UpdateDatabaseCompaction(t *testing.T) {
	tr, cleanup := connectDatabaseBarrierInProcess(t, newMockDatabaseSvc())
	defer cleanup()

	// The revision policy and the off switch run in
	// conformance TestCompactionPolicy_OnEveryTransport; the period field
	// and a nil policy are checked here.
	info, err := tr.UpdateDatabaseCompaction(context.Background(), transport.UpdateDatabaseCompactionRequest{
		Name: "mydb", Compaction: &transport.DatabaseCompactionConfig{Mode: "period", RetainPeriod: "3h"},
	})
	if err != nil || info.Compaction == nil || info.Compaction.RetainPeriod != "3h" {
		t.Fatalf("period = %+v, %v", info, err)
	}

	// A nil policy turns it off, as on every other transport.
	info, err = tr.UpdateDatabaseCompaction(context.Background(), transport.UpdateDatabaseCompactionRequest{Name: "mydb"})
	if err != nil || info.Compaction != nil {
		t.Fatalf("off = %+v, %v", info, err)
	}
}
