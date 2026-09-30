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

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// The SQL service is built once, over the KV and database services the
// transport was constructed with, and cached for the transport's lifetime.
func TestEmbeddedSQL_ServiceIsCachedAfterFirstUse(t *testing.T) {
	dbSvc := service.NewDatabaseService(database.NewManager(nil, t.TempDir()))
	tr := newExtTransport(t, WithDatabaseService(dbSvc))

	svc := tr.sqlSvc()
	if svc == nil {
		t.Fatal("sqlSvc() = nil with a KV and a database service configured")
	}
	if tr.sqlSvc() != svc {
		t.Fatal("sqlSvc() must return the same cached service on every call")
	}
}

// Without a database service there is nothing to build a SQL service over.
func TestEmbeddedSQL_NoDatabaseServiceNoSQLService(t *testing.T) {
	if svc := newExtTransport(t).sqlSvc(); svc != nil {
		t.Fatalf("sqlSvc() = %v, want nil without a database service", svc)
	}
}
