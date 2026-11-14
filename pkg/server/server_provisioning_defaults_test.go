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

package server

import (
	"io"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// defaultsStubServicer is a database service that cannot resolve defaults.
type defaultsStubServicer struct {
	service.DatabaseServicer
}

// TestProvisioningDefaults_ResolvesThroughTheDatabaseService: with no manager
// option, the provisioning check still sees the node's database_defaults,
// through the service that performs the create.
func TestProvisioningDefaults_ResolvesThroughTheDatabaseService(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := databasetest.NewManager(logger, t.TempDir(), database.WithDefaults(database.Defaults{Backend: database.BackendRaft}))
	t.Cleanup(func() { _ = mgr.Close() })
	svc := service.NewDatabaseService(mgr)

	if got := provisioningDefaults(svc, nil)(database.DatabaseConfig{Name: "a"}).Backend; got != database.BackendRaft {
		t.Fatalf("resolved backend = %q, want %q from the service's manager defaults", got, database.BackendRaft)
	}
}

// TestProvisioningDefaults_FailsClosedWithoutADefaultsSource: with neither a
// resolving service nor a manager, a create that leaves its backend out is
// checked as a raft create; an explicit backend is left as it is.
func TestProvisioningDefaults_FailsClosedWithoutADefaultsSource(t *testing.T) {
	resolve := provisioningDefaults(defaultsStubServicer{}, nil)

	if got := resolve(database.DatabaseConfig{Name: "a"}).Backend; got != database.BackendRaft {
		t.Fatalf("resolved backend = %q, want %q (fail closed)", got, database.BackendRaft)
	}
	if got := resolve(database.DatabaseConfig{Name: "a", Backend: database.BackendPebble}).Backend; got != database.BackendPebble {
		t.Fatalf("explicit backend resolved to %q, want %q", got, database.BackendPebble)
	}
}

// TestProvisioningDefaults_FallsBackToTheManager: a service that cannot
// resolve defaults defers to the manager option.
func TestProvisioningDefaults_FallsBackToTheManager(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := databasetest.NewManager(logger, t.TempDir(), database.WithDefaults(database.Defaults{Backend: database.BackendPebble}))
	t.Cleanup(func() { _ = mgr.Close() })

	if got := provisioningDefaults(defaultsStubServicer{}, mgr)(database.DatabaseConfig{Name: "a"}).Backend; got != database.BackendPebble {
		t.Fatalf("resolved backend = %q, want %q from the manager defaults", got, database.BackendPebble)
	}
}
