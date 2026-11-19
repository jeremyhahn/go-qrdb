//go:build qrdb_sql

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

package embedded

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// With a database service the SQL service is built on first use and the
// same instance is reused on every later call, so its engine cache (and the
// analyzer catalog behind it) survives across queries.
func TestEmbeddedSQL_ServiceIsCachedAfterFirstUse(t *testing.T) {
	dbSvc := service.NewDatabaseService(databasetest.NewManager(nil, t.TempDir()))
	tr := newExtTransport(t, WithDatabaseService(dbSvc))

	first := tr.sqlSvc()
	if first == nil {
		t.Fatal("sqlSvc() = nil, want a SQL service over the configured database service")
	}
	if second := tr.sqlSvc(); second != first {
		t.Fatalf("second sqlSvc() = %p, want the cached instance %p", second, first)
	}
}

// Without a database service there is nothing to build a SQL service over.
func TestEmbeddedSQL_NoDatabaseServiceNoSQLService(t *testing.T) {
	if svc := newExtTransport(t).sqlSvc(); svc != nil {
		t.Fatalf("sqlSvc() = %v, want nil without a database service", svc)
	}
}
