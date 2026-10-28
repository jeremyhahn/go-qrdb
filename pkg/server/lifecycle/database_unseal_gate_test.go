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

package lifecycle

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal/clustersealtest"
)

// TestDatabaseUnsealGate_VetoesASealedRecord: the node unseal's database veto
// follows the cluster seal record of the database its barrier is keyed by,
// named by its tenant (the system tenant for an unowned database), so one
// tenant's sealed record never vetoes another tenant's database of the same
// name.
func TestDatabaseUnsealGate_VetoesASealedRecord(t *testing.T) {
	sealer := clustersealtest.New(true)
	gate := databaseUnsealGate(sealer)

	if gate("orders", "acme") || gate("orders", "beta") || gate("platform", "") {
		t.Fatal("a database with no sealed record was vetoed")
	}
	sealer.Put(clusterseal.Record{Barrier: clusterseal.DatabaseBarrier("acme", "orders"), Epoch: 1, Sealed: true})
	sealer.Put(clusterseal.Record{Barrier: clusterseal.DatabaseBarrier("system", "platform"), Epoch: 1, Sealed: true})
	if !gate("orders", "acme") {
		t.Fatal("a database whose record is sealed was not vetoed")
	}
	if gate("orders", "beta") {
		t.Fatal("another tenant's sealed record vetoed a database of the same name")
	}
	if !gate("platform", "") {
		t.Fatal("an unowned database's record, the system tenant's, was not consulted")
	}
}
