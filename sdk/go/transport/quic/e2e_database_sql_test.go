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

package quic

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestE2E_UpdateDatabaseSQL_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("db.UpdateSQL", func(payload []byte) (uint16, []byte) {
		var req struct {
			Name string                       `json:"name"`
			SQL  *transport.DatabaseSQLConfig `json:"sql"`
		}
		if err := json.Unmarshal(payload, &req); err != nil || req.Name != "shop" || req.SQL == nil {
			return wire.StatusInvalidArgument, nil
		}
		b, _ := json.Marshal(transport.DatabaseInfo{Name: req.Name, SQL: req.SQL})
		return wire.StatusOK, b
	})
	resp, err := tr.UpdateDatabaseSQL(context.Background(), transport.UpdateDatabaseSQLRequest{
		Name: "shop", SQL: &transport.DatabaseSQLConfig{Concurrency: "pessimistic"},
	})
	if err != nil {
		t.Fatalf("UpdateDatabaseSQL: %v", err)
	}
	if resp.SQL == nil || resp.SQL.Concurrency != "pessimistic" {
		t.Fatalf("SQL = %+v, want pessimistic", resp.SQL)
	}
}

func TestE2E_UpdateDatabaseSQL_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("db.UpdateSQL", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, []byte("not json")
	})
	if _, err := tr.UpdateDatabaseSQL(context.Background(), transport.UpdateDatabaseSQLRequest{Name: "shop"}); err == nil {
		t.Fatal("expected an unmarshal error")
	}
}

func TestE2E_UpdateDatabaseSQL_Closed(t *testing.T) {
	_, tr := startE2EServer(t)
	_ = tr.Close()
	var closed *transport.ClosedError
	if _, err := tr.UpdateDatabaseSQL(context.Background(), transport.UpdateDatabaseSQLRequest{Name: "shop"}); !errors.As(err, &closed) {
		t.Fatalf("closed transport = %v, want *ClosedError", err)
	}
}
