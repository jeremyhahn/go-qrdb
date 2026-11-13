//go:build !qrdb_fts

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

package fts

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestStubAvailable confirms the no-op stub reports FTS as unavailable when
// the qrdb_fts build tag is not set. This is the build the default test and
// coverage runs compile, so without this test the stub is reported at 0%.
func TestStubAvailable(t *testing.T) {
	assert.False(t, Available(), "Available must be false when qrdb_fts is not compiled in")
}
