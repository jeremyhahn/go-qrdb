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
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// TestDatabaseService_VolumeTenant_ReservedHostDirectoryNames pins SEC-9: a
// volume-isolated tenant's database is stored at
// {DataRoot}/databases/<name>, the directory that also holds the tenant's own
// Raft host -- its WAL at databases/wal and its node-host state at
// databases/nodehost. A database named after either was created inside that
// state. The names must be refused with a typed error before any directory is
// made.
func TestDatabaseService_VolumeTenant_ReservedHostDirectoryNames(t *testing.T) {
	for _, name := range []string{"wal", "nodehost", "WAL"} {
		t.Run(name, func(t *testing.T) {
			dataRoot := t.TempDir()
			mgr := databasetest.NewManager(nil, t.TempDir())
			t.Cleanup(func() { _ = mgr.Close() })
			lookup := &fakeTenantInfoLookup{info: &tenant.TenantInfo{
				ID:            "acme",
				IsolationType: tenant.IsolationVolume,
				VolumePool:    "default",
			}}
			svc := NewDatabaseService(mgr, WithTenantInfoLookup(lookup),
				WithTenantDataRootResolver(&fakeDataRootResolver{roots: map[string]string{"acme": dataRoot}}))
			ctx := tenant.WithTenantID(context.Background(), "acme")

			_, err := svc.CreateForTenant(ctx, testDBConfig(name))
			var reserved *database.ReservedNameError
			if !errors.As(err, &reserved) {
				t.Fatalf("CreateForTenant(%q) error = %v (%T), want *database.ReservedNameError", name, err, err)
			}
			if _, statErr := os.Stat(filepath.Join(dataRoot, "databases", name)); !os.IsNotExist(statErr) {
				t.Fatalf("the refused create touched %s: %v", filepath.Join(dataRoot, "databases", name), statErr)
			}
		})
	}
}
