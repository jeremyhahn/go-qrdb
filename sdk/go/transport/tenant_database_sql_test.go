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

package transport

import (
	"context"
	"errors"
	"testing"
)

func TestTenantDatabaseClient_UpdateSQL_Success(t *testing.T) {
	mock := newMockDatabaseClient()
	mock.databases["mydb"] = &DatabaseInfo{TenantID: "acme", Name: "mydb", Status: "open"}

	tc, _ := WithTenantDatabase(mock, "acme")
	info, err := tc.UpdateDatabaseSQL(context.Background(), UpdateDatabaseSQLRequest{
		Name: "mydb",
		SQL:  &DatabaseSQLConfig{Concurrency: "pessimistic"},
	})
	if err != nil {
		t.Fatalf("UpdateDatabaseSQL failed: %v", err)
	}
	if info.SQL == nil || info.SQL.Concurrency != "pessimistic" {
		t.Fatalf("SQL = %+v, want pessimistic", info.SQL)
	}
}

func TestTenantDatabaseClient_UpdateSQL_CrossTenantDenied(t *testing.T) {
	mock := newMockDatabaseClient()
	mock.databases["otherdb"] = &DatabaseInfo{TenantID: "other-tenant", Name: "otherdb", Status: "open"}

	tc, _ := WithTenantDatabase(mock, "acme")
	_, err := tc.UpdateDatabaseSQL(context.Background(), UpdateDatabaseSQLRequest{
		Name: "otherdb",
		SQL:  &DatabaseSQLConfig{Concurrency: "pessimistic"},
	})
	var accessErr *TenantDatabaseAccessDeniedError
	if !errors.As(err, &accessErr) {
		t.Fatalf("expected TenantDatabaseAccessDeniedError, got %v", err)
	}
}
