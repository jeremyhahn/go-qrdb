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

package unix

import (
	"context"
	"errors"
	"testing"

	unixserver "github.com/jeremyhahn/go-qrdb/pkg/api/unix"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestUnixRT_UpdateDatabaseCompaction(t *testing.T) {
	svc := &mockDatabaseSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithDatabaseService(svc))
	defer cleanup()

	resp, err := tr.UpdateDatabaseCompaction(context.Background(), transport.UpdateDatabaseCompactionRequest{
		Name: "db1", Compaction: &transport.DatabaseCompactionConfig{Mode: "period", RetainPeriod: "1h"},
	})
	if err != nil {
		t.Fatalf("UpdateDatabaseCompaction: %v", err)
	}
	if resp.Compaction == nil || resp.Compaction.Mode != "period" || resp.Compaction.RetainPeriod != "1h" {
		t.Fatalf("Compaction = %+v", resp.Compaction)
	}

	_ = tr.Close()
	var closed *transport.ClosedError
	if _, err := tr.UpdateDatabaseCompaction(context.Background(), transport.UpdateDatabaseCompactionRequest{Name: "db1"}); !errors.As(err, &closed) {
		t.Fatalf("closed = %v, want *ClosedError", err)
	}
}
