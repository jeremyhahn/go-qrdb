// Copyright (c) 2026 Jeremy Hahn
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

package objstoretest

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	objcommon "github.com/jeremyhahn/go-objstore/pkg/common"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// fatalRecorder is a testing.TB whose Fatalf records the failure instead of
// ending the goroutine, so the refusal path of NewProvider is observable.
type fatalRecorder struct {
	testing.TB
	fatal string
}

func (r *fatalRecorder) Helper() {}

func (r *fatalRecorder) Fatalf(format string, args ...any) {
	r.fatal = fmt.Sprintf(format, args...)
}

func TestNewProvider_FailsTheTestWhenTheSeamRefusesTheName(t *testing.T) {
	first := NewProvider(t)
	// Rewinding the sequence makes the next name one the factory already
	// serves, which the seam refuses.
	seq.Add(^uint64(0))
	rec := &fatalRecorder{TB: t}
	again := NewProvider(rec)
	assert.Equal(t, first.Name(), again.Name())
	assert.Contains(t, rec.fatal, first.Name())
	assert.Contains(t, rec.fatal, "already registered")
}

func TestProvider_OpenFailsOnADestroyedCredential(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	require.NoError(t, os.WriteFile(path, []byte("fake-secret-access-key"), 0o600))
	h, err := secure.ReadFileHandle(path)
	require.NoError(t, err)
	require.NoError(t, h.Destroy())

	p := &Provider{name: "unregistered"}
	store, err := p.open(map[string]string{}, objcommon.Credentials{Secret: h})
	require.Error(t, err)
	assert.Nil(t, store)
	assert.Empty(t, p.Deliveries(), "a credential that cannot be read is not recorded as delivered")
}
