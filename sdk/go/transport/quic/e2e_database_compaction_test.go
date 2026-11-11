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

func TestE2E_UpdateDatabaseCompaction_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("db.UpdateCompaction", func(payload []byte) (uint16, []byte) {
		var req struct {
			Name       string                              `json:"name"`
			Compaction *transport.DatabaseCompactionConfig `json:"compaction"`
		}
		if err := json.Unmarshal(payload, &req); err != nil || req.Name != "events" || req.Compaction == nil {
			return wire.StatusInvalidArgument, nil
		}
		b, _ := json.Marshal(transport.DatabaseInfo{Name: req.Name, Compaction: req.Compaction})
		return wire.StatusOK, b
	})
	resp, err := tr.UpdateDatabaseCompaction(context.Background(), transport.UpdateDatabaseCompactionRequest{
		Name: "events", Compaction: &transport.DatabaseCompactionConfig{Mode: "revision", RetainRevisions: 8},
	})
	if err != nil {
		t.Fatalf("UpdateDatabaseCompaction: %v", err)
	}
	if resp.Compaction == nil || resp.Compaction.Mode != "revision" || resp.Compaction.RetainRevisions != 8 {
		t.Fatalf("Compaction = %+v", resp.Compaction)
	}
}

func TestE2E_UpdateDatabaseCompaction_Errors(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("db.UpdateCompaction", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, []byte("not json")
	})
	if _, err := tr.UpdateDatabaseCompaction(context.Background(), transport.UpdateDatabaseCompactionRequest{Name: "events"}); err == nil {
		t.Fatal("expected an unmarshal error")
	}
	srv.SetResponder("db.UpdateCompaction", func(_ []byte) (uint16, []byte) {
		return wire.StatusPermissionDenied, nil
	})
	if _, err := tr.UpdateDatabaseCompaction(context.Background(), transport.UpdateDatabaseCompactionRequest{Name: "events"}); err == nil {
		t.Fatal("a refused call reported success")
	}
	_ = tr.Close()
	var closed *transport.ClosedError
	if _, err := tr.UpdateDatabaseCompaction(context.Background(), transport.UpdateDatabaseCompactionRequest{Name: "events"}); !errors.As(err, &closed) {
		t.Fatalf("closed transport = %v, want *ClosedError", err)
	}
}
