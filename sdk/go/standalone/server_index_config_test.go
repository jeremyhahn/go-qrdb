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

package standalone

import (
	"context"
	"errors"
	"strconv"
	"testing"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// newIndexConfigServer starts a memory-backed server with index config ic,
// shut down at test end.
func newIndexConfigServer(t *testing.T, ic qrdbconfig.IndexConfig) *Server {
	t.Helper()
	srv, err := NewServer(context.Background(), &ServerConfig{Backend: database.BackendMemory, Index: ic})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	return srv
}

// createClientIndexes creates n indexes through srv's client and returns the
// first refusal (nil when all n are created).
func createClientIndexes(srv *Server, n int) error {
	for i := 0; i < n; i++ {
		if err := srv.Client().Index().IndexCreate(context.Background(), "idx-"+strconv.Itoa(i), "f", transport.IndexConfig{}); err != nil {
			return err
		}
	}
	return nil
}

func TestNewServer_MaxIndexesRefusesTheIndexPastTheCap(t *testing.T) {
	srv := newIndexConfigServer(t, qrdbconfig.IndexConfig{MaxIndexes: 2})
	if err := createClientIndexes(srv, 2); err != nil {
		t.Fatalf("indexes under the cap: %v", err)
	}
	var le *service.IndexLimitError
	if err := srv.Client().Index().IndexCreate(context.Background(), "idx-extra", "f", transport.IndexConfig{}); !errors.As(err, &le) {
		t.Fatalf("third index at max_indexes 2: err = %v (%T), want *service.IndexLimitError", err, err)
	}
}

func TestNewServer_ZeroIndexConfigLeavesIndexesUnlimited(t *testing.T) {
	srv := newIndexConfigServer(t, qrdbconfig.IndexConfig{})
	if err := createClientIndexes(srv, 8); err != nil {
		t.Fatalf("max_indexes 0 must not cap: %v", err)
	}
}
