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

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
)

func TestHandleDatabaseCompaction_UpdateAndClear(t *testing.T) {
	srv := newSQLConfigQUICServer(t)
	ctx := context.Background()

	create, _ := json.Marshal(map[string]any{"name": "events", "engine": "kv", "backend": "memory"})
	if _, err := srv.handleDatabaseCreate(ctx, create); err != nil {
		t.Fatalf("handleDatabaseCreate: %v", err)
	}

	update, _ := json.Marshal(map[string]any{"name": "events", "compaction": map[string]any{"mode": "revision", "retainRevisions": 4}})
	resp, err := srv.handleDatabaseUpdateCompaction(ctx, update)
	if err != nil {
		t.Fatalf("handleDatabaseUpdateCompaction: %v", err)
	}
	var info database.DatabaseInfo
	if err := json.Unmarshal(resp, &info); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if info.Compaction == nil || info.Compaction.Mode != compaction.Revision || info.Compaction.RetainRevisions != 4 {
		t.Fatalf("updated compaction = %+v", info.Compaction)
	}

	clear, _ := json.Marshal(map[string]any{"name": "events"})
	resp, err = srv.handleDatabaseUpdateCompaction(ctx, clear)
	if err != nil {
		t.Fatalf("handleDatabaseUpdateCompaction(clear): %v", err)
	}
	info = database.DatabaseInfo{}
	if err := json.Unmarshal(resp, &info); err != nil || info.Compaction != nil {
		t.Fatalf("cleared compaction = %+v, %v", info.Compaction, err)
	}
}

func TestHandleDatabaseCompaction_Refusals(t *testing.T) {
	srv := newSQLConfigQUICServer(t)
	ctx := context.Background()

	if _, err := srv.handleDatabaseUpdateCompaction(ctx, []byte("{")); err == nil {
		t.Fatal("malformed JSON was accepted")
	}
	var invalid *InvalidArgumentError
	if _, err := srv.handleDatabaseUpdateCompaction(ctx, []byte(`{"compaction":{"mode":"off"}}`)); !errors.As(err, &invalid) {
		t.Fatalf("missing name = %v", err)
	}
	var nf *database.NotFoundError
	if _, err := srv.handleDatabaseUpdateCompaction(ctx, []byte(`{"name":"nope","compaction":{"mode":"off"}}`)); !errors.As(err, &nf) {
		t.Fatalf("missing database = %v", err)
	}

	bare := NewServer(nil, nil, "localhost:0")
	var notConfigured *ServiceNotConfiguredError
	if _, err := bare.handleDatabaseUpdateCompaction(ctx, []byte(`{"name":"x"}`)); !errors.As(err, &notConfigured) {
		t.Fatalf("no database service = %v", err)
	}
}
