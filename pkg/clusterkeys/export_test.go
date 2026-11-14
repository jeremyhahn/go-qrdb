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

package clusterkeys

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/keydelivery"
)

// HTTPCodeForOpenErr exposes httpCodeForOpenErr to the external tests.
var HTTPCodeForOpenErr = httpCodeForOpenErr

// SetModeImporterForTest registers an importer for mode until the test ends.
func SetModeImporterForTest(t *testing.T, mode keydelivery.Mode, fn func(ctx context.Context, meta keydelivery.Meta, plaintext []byte) error) {
	t.Helper()
	modeHandlers[mode] = func(ctx context.Context, _ *Receiver, meta keydelivery.Meta, plaintext []byte) error {
		return fn(ctx, meta, plaintext)
	}
	t.Cleanup(func() { delete(modeHandlers, mode) })
}
