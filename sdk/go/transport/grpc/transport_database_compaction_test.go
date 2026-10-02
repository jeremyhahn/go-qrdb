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

package grpc

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestGRPCDatabase_UpdateDatabaseCompaction_RoundTrip(t *testing.T) {
	addr, stop := startDatabaseServer(t)
	defer stop()
	tr, cleanup := connectDatabaseTransport(t, addr)
	defer cleanup()

	info, err := tr.UpdateDatabaseCompaction(context.Background(), transport.UpdateDatabaseCompactionRequest{
		Name: "testdb", Compaction: &transport.DatabaseCompactionConfig{Mode: "revision", RetainRevisions: 6},
	})
	if err != nil {
		t.Fatalf("UpdateDatabaseCompaction: %v", err)
	}
	if info.Compaction == nil || info.Compaction.Mode != "revision" || info.Compaction.RetainRevisions != 6 {
		t.Fatalf("Compaction = %+v", info.Compaction)
	}

	info, err = tr.UpdateDatabaseCompaction(context.Background(), transport.UpdateDatabaseCompactionRequest{Name: "testdb"})
	if err != nil || info.Compaction != nil {
		t.Fatalf("clear = %+v, %v", info, err)
	}
}

func TestGRPCDatabase_UpdateDatabaseCompaction_Closed(t *testing.T) {
	addr, stop := startDatabaseServer(t)
	defer stop()
	tr, cleanup := connectDatabaseTransport(t, addr)
	defer cleanup()
	tr.closed.Store(true)
	var closed *transport.ClosedError
	if _, err := tr.UpdateDatabaseCompaction(context.Background(), transport.UpdateDatabaseCompactionRequest{Name: "testdb"}); !errors.As(err, &closed) {
		t.Fatalf("closed = %v, want *ClosedError", err)
	}
}
