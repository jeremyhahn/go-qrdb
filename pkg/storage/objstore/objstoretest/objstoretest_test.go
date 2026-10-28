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

package objstoretest_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/objstore"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/objstore/objstoretest"
)

func writeFile(t *testing.T, name, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	return path
}

func open(t *testing.T, p *objstoretest.Provider, files objstore.CredentialFiles) {
	t.Helper()
	e, err := objstore.New(p.Name(), map[string]string{}, objstore.WithCredentialFiles(files))
	require.NoError(t, err)
	t.Cleanup(func() { _ = e.Close() })
	require.NoError(t, e.Set([]byte("k"), []byte("v")))
}

func TestNewProvider_NamesEveryProviderApart(t *testing.T) {
	a, b := objstoretest.NewProvider(t), objstoretest.NewProvider(t)
	assert.NotEmpty(t, a.Name())
	assert.NotEqual(t, a.Name(), b.Name())
	assert.Empty(t, a.Deliveries(), "a provider never opened has received nothing")
}

func TestProvider_ReceivesTheReferencedFileContents(t *testing.T) {
	p := objstoretest.NewProvider(t)
	open(t, p, objstore.CredentialFiles{
		KeyFile:    writeFile(t, "key", "FAKEACCESSKEYID0000"),
		SecretFile: writeFile(t, "secret", "fake-secret-access-key"),
	})
	got := p.Deliveries()
	require.Len(t, got, 1)
	assert.Equal(t, "FAKEACCESSKEYID0000", got[0].Key)
	requireSecret(t, "fake-secret-access-key", got[0].Secret)
}

func TestProvider_ReceivesNothingWithoutAReference(t *testing.T) {
	p := objstoretest.NewProvider(t)
	open(t, p, objstore.CredentialFiles{})
	assert.Equal(t, []objstoretest.Delivery{{}}, p.Deliveries())
}

func TestProvider_DeliveriesIsACopy(t *testing.T) {
	p := objstoretest.NewProvider(t)
	open(t, p, objstore.CredentialFiles{SecretFile: writeFile(t, "secret", "fake-secret-access-key")})
	got := p.Deliveries()
	require.Len(t, got, 1)
	got[0].Secret = nil
	requireSecret(t, "fake-secret-access-key", p.Deliveries()[0].Secret)
}

func TestProvider_DestroysTheSecretsWhenTheTestEnds(t *testing.T) {
	var got []objstoretest.Delivery
	t.Run("records", func(t *testing.T) {
		p := objstoretest.NewProvider(t)
		open(t, p, objstore.CredentialFiles{SecretFile: writeFile(t, "secret", "fake-secret-access-key")})
		got = p.Deliveries()
		require.Len(t, got, 1)
		assert.True(t, got[0].Secret.IsAlive(), "the secret lives while its test runs")
	})
	assert.False(t, got[0].Secret.IsAlive(), "the secret is destroyed when its test ends")
}

func requireSecret(t *testing.T, want string, got *secure.Buffer) {
	t.Helper()
	require.NotNil(t, got)
	ok, err := got.EqualBytes([]byte(want))
	require.NoError(t, err)
	assert.True(t, ok, "the recorded secret is the referenced file's contents")
}
