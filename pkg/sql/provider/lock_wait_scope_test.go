//go:build qrdb_sql

// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package provider

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// innodb_lock_wait_timeout is a session setting only. The engine's global
// variables are process-wide -- shared by every tenant on the node -- so a
// GLOBAL scope would let one tenant change every other tenant's default.
func TestLockWaitTimeout_GlobalScopeIsRefused(t *testing.T) {
	h := newLockHarness(t)
	c := h.conn(t)
	_, err := c.exec("SET GLOBAL innodb_lock_wait_timeout = 5")
	require.Error(t, err)

	fresh := h.conn(t)
	rows := fresh.must("SELECT @@innodb_lock_wait_timeout")
	require.Len(t, rows, 1)
	assert.EqualValues(t, DefaultLockWaitTimeoutSeconds, rows[0][0])

	// The session form still works.
	c.must("SET SESSION innodb_lock_wait_timeout = 7")
	assert.EqualValues(t, 7, c.must("SELECT @@innodb_lock_wait_timeout")[0][0])
	assert.EqualValues(t, DefaultLockWaitTimeoutSeconds, fresh.must("SELECT @@innodb_lock_wait_timeout")[0][0])
}
