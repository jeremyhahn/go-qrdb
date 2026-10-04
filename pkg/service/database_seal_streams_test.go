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

package service

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// TestSealDatabase_NodeLocalEndsTheDatabasesStreams: before cluster sealing
// is active a database seal is node-local, and it still ends the database's
// open streams and its tenant's SQL sessions -- both subscribe to the node's
// stream registry -- and leaves another database's streams.
func TestSealDatabase_NodeLocalEndsTheDatabasesStreams(t *testing.T) {
	svc, _ := newTestDatabaseServiceWithBarrier(t)
	deferred := clusterseal.NewDeferred() // inactive: seals are node-local
	svc.sealer = deferred
	ctx := tenant.WithTenantID(context.Background(), tenantid.System)
	cfg := testDBConfig("secure")
	cfg.DataDir = t.TempDir()
	cfg.Barrier = &database.DatabaseBarrierConfig{Enabled: true, Mode: database.BarrierModePassphrase}
	if _, err := svc.CreateForTenantWithCredentials(ctx, cfg, seal.MustCredentials(context.Background(), []byte("s3cret-passphrase"))); err != nil {
		t.Fatal(err)
	}
	streams := deferred.Streams()
	watch, err := streams.SubscribeServing(tenantid.System, "secure", nil)
	if err != nil {
		t.Fatal(err)
	}
	session, err := streams.SubscribeTenantServing(tenantid.System, nil)
	if err != nil {
		t.Fatal(err)
	}
	other, err := streams.SubscribeServing(tenantid.System, "other", nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.SealDatabase(ctx, "secure"); err != nil {
		t.Fatalf("SealDatabase: %v", err)
	}
	for name, sub := range map[string]*clusterseal.Subscription{"watch": watch, "SQL session": session} {
		select {
		case <-sub.Done():
		default:
			t.Fatalf("the database's %s outlived its node-local seal", name)
		}
	}
	select {
	case <-other.Done():
		t.Fatal("another database's stream was ended")
	default:
	}
}
