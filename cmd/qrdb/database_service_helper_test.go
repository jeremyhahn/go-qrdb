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

package main

import (
	"log/slog"
	"os"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/service/servicetest"
)

// newRealCLIDatabaseService builds a DatabaseServicer backed by a real
// database.Manager rooted at a temporary directory, for tests that need
// actual create/update persistence rather than a function-double. Carries
// no build tag: database_compaction_test.go (unconditional) and
// database_sql_test.go (qrdb_sql) both call it, so its own constraint must
// be the union of theirs -- none -- even though only one caller is tagged.
func newRealCLIDatabaseService(t *testing.T) service.DatabaseServicer {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := databasetest.NewManager(logger, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	return servicetest.NewDatabaseService(t, mgr)
}
