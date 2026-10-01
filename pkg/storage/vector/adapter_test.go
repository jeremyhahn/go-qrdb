// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package vector

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/interfaces"
)

// testLogger is a no-op logger for testing.
type testLogger struct{}

func (l *testLogger) Debug(msg string, args ...any)                              {}
func (l *testLogger) Debugf(format string, args ...interface{})                  {}
func (l *testLogger) Info(msg string, args ...any)                               {}
func (l *testLogger) Infof(format string, args ...interface{})                   {}
func (l *testLogger) Warn(msg string, args ...any)                               {}
func (l *testLogger) Warnf(format string, args ...interface{})                   {}
func (l *testLogger) Error(msg string, args ...any)                              {}
func (l *testLogger) Errorf(format string, args ...interface{})                  {}
func (l *testLogger) Fatal(msg string)                                           {}
func (l *testLogger) Fatalf(format string, args ...interface{})                  {}
func (l *testLogger) WithField(key string, value interface{}) interfaces.Logger  { return l }
func (l *testLogger) WithFields(fields map[string]interface{}) interfaces.Logger { return l }
func (l *testLogger) WithError(err error) interfaces.Logger                      { return l }

// setupStorage creates a test PebbleVectorStorage with a temp directory.
// Returns the storage and a cleanup function.
func setupStorage(t *testing.T) (*PebbleVectorStorage, func()) {
	t.Helper()
	tmpDir := t.TempDir()

	config := DefaultConfig(tmpDir)
	log := &testLogger{}

	storage, err := NewPebbleVectorStorage(config, log)
	require.NoError(t, err)

	cleanup := func() {
		_ = storage.Close()
		_ = os.RemoveAll(tmpDir)
	}
	return storage, cleanup
}
