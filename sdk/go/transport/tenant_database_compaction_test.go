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

func TestTenantDatabaseClient_UpdateCompaction_Success(t *testing.T) {
	mock := newMockDatabaseClient()
	mock.databases["mydb"] = &DatabaseInfo{TenantID: "acme", Name: "mydb", Status: "open"}

	tc, _ := WithTenantDatabase(mock, "acme")
	info, err := tc.UpdateDatabaseCompaction(context.Background(), UpdateDatabaseCompactionRequest{
		Name:       "mydb",
		Compaction: &DatabaseCompactionConfig{Mode: "revision", RetainRevisions: 3},
	})
	if err != nil {
		t.Fatalf("UpdateDatabaseCompaction failed: %v", err)
	}
	if info.Compaction == nil || info.Compaction.RetainRevisions != 3 {
		t.Fatalf("Compaction = %+v", info.Compaction)
	}
}

func TestTenantDatabaseClient_UpdateCompaction_CrossTenantDenied(t *testing.T) {
	mock := newMockDatabaseClient()
	mock.databases["otherdb"] = &DatabaseInfo{TenantID: "other-tenant", Name: "otherdb", Status: "open"}

	tc, _ := WithTenantDatabase(mock, "acme")
	_, err := tc.UpdateDatabaseCompaction(context.Background(), UpdateDatabaseCompactionRequest{
		Name:       "otherdb",
		Compaction: &DatabaseCompactionConfig{Mode: "revision", RetainRevisions: 1},
	})
	var accessErr *TenantDatabaseAccessDeniedError
	if !errors.As(err, &accessErr) {
		t.Fatalf("expected TenantDatabaseAccessDeniedError, got %v", err)
	}
	if mock.databases["otherdb"].Compaction != nil {
		t.Fatal("the other tenant's policy was set")
	}
}
