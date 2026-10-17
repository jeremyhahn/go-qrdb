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

//go:build integration

package integration_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
)

// TestMain removes the qrdbd and qrdb binaries the package's tests compiled
// through qrdbdharness.BuildBinary once every test has run. Their cache
// outlives each test, so no t.Cleanup can remove them, and each is a full
// race-built binary left in os.TempDir.
func TestMain(m *testing.M) {
	code := m.Run()
	if err := qrdbdharness.RemoveBuilds(); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(code)
}
