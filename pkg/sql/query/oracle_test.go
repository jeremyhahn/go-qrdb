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

package query

import (
	"errors"
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// Database names are one node-wide catalog, so the catalog knows another
// tenant's database while the registry answers it as missing for this
// tenant. Consulting the catalog first answered a name nobody holds with the
// catalog's not-found and another tenant's name with the registry's -- two
// different errors, which let a caller probe which names other tenants hold.
// With a registry, both must be the registry's answer.
func TestExecute_WithRegistry_ForeignDatabaseAnsweredAsMissing(t *testing.T) {
	reg := &mockRegistry{
		kvServiceFn: func(tenantID, db string) (service.KVServicer, error) {
			if tenantID == "acme" && db == "mine" {
				return newMockKV(), nil
			}
			return nil, &service.DatabaseNotFoundError{TenantID: tenantID, Database: db}
		},
	}
	catalog := &mockDB{getFn: func(name string) (*database.DatabaseInfo, error) {
		if name == "nosuch" {
			return nil, &database.NotFoundError{Name: name}
		}
		return kvDB(name)
	}}
	svc := NewService(newMockKV(), catalog, WithRegistry(reg))
	ctx := authedContext("acme")

	_, foreignErr := svc.Execute(ctx, "theirs", "SELECT 1")
	_, missingErr := svc.Execute(ctx, "nosuch", "SELECT 1")
	var notFound *service.DatabaseNotFoundError
	if !errors.As(foreignErr, &notFound) || !errors.As(missingErr, &notFound) {
		t.Fatalf("foreign = %T %v, missing = %T %v; want *service.DatabaseNotFoundError for both",
			foreignErr, foreignErr, missingErr, missingErr)
	}
	if reflect.TypeOf(foreignErr) != reflect.TypeOf(missingErr) {
		t.Fatalf("foreign answered %T, missing %T: the difference is an oracle", foreignErr, missingErr)
	}

	// The tenant's own database still resolves.
	if _, err := svc.Execute(ctx, "mine", "SELECT 1"); err != nil {
		t.Fatalf("own database: %v", err)
	}
}
