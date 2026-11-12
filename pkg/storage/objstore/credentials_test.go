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

package objstore

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	objcommon "github.com/jeremyhahn/go-objstore/pkg/common"
	objmemory "github.com/jeremyhahn/go-objstore/pkg/memory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// Fake credential material: never a real key.
const (
	fakeKey    = "FAKEACCESSKEYID0000"
	fakeSecret = "fake-secret-access-key-for-tests"
)

// writeCredentialFile writes a fake credential to an owner-only file.
func writeCredentialFile(t *testing.T, name, value string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte(value+"\n"), 0o600))
	return path
}

// fakeFiles returns CredentialFiles referencing a fake key and secret.
func fakeFiles(t *testing.T) CredentialFiles {
	t.Helper()
	return CredentialFiles{
		KeyFile:    writeCredentialFile(t, "key", fakeKey),
		SecretFile: writeCredentialFile(t, "secret", fakeSecret),
	}
}

// credProbeProvider is the go-objstore storage provider registerCredentialProbe
// registers: a memory store whose creator checks the credentials New passes.
const credProbeProvider = "qrdb-credential-probe"

// errProbeConfigure is the failure a probe configured to fail returns.
var errProbeConfigure = errors.New("credential probe: configure failed")

// credentialProbe records what the probe provider was given. seen keeps the
// Credentials so a test can check, after New returns, that New destroyed
// the handles it supplied.
type credentialProbe struct {
	reads               credentialReads
	fail                error
	seen                []objcommon.Credentials
	liveDuringConfigure bool
	heldTheFiles        bool
}

// registerCredentialProbe registers the probe provider, reading the
// credentials reads names, for the duration of the test. go-objstore's
// registry has no removal, so the creator stays registered; the
// providerCredentials entry that admits credentials to it does not.
func registerCredentialProbe(t *testing.T, reads credentialReads) *credentialProbe {
	t.Helper()
	p := &credentialProbe{reads: reads}
	registeredProviders.Store(credProbeProvider, registeredProvider{
		reads: reads,
		creator: func(settings map[string]string, creds objcommon.Credentials) (objcommon.Storage, error) {
			p.configure(t, creds)
			if p.fail != nil {
				return nil, p.fail
			}
			store := objmemory.New()
			if err := store.Configure(settings, creds); err != nil {
				return nil, err
			}
			return store, nil
		},
	})
	t.Cleanup(func() { registeredProviders.Delete(credProbeProvider) })
	return p
}

// configure checks, while the provider is configuring, that the handles it
// was given are live and hold the referenced files, as a provider reading
// them would see them.
func (p *credentialProbe) configure(t *testing.T, creds objcommon.Credentials) {
	t.Helper()
	p.seen = append(p.seen, creds)
	p.liveDuringConfigure = creds.Secret != nil && creds.Secret.IsAlive() &&
		(!p.reads.key || creds.Key != nil && creds.Key.IsAlive())
	if !p.liveDuringConfigure {
		return
	}
	held, err := creds.Secret.EqualTo([]byte(fakeSecret))
	require.NoError(t, err)
	if held && p.reads.key {
		held = false
		require.NoError(t, creds.Open(func(key, secret []byte) error {
			held = string(key) == fakeKey && string(secret) == fakeSecret
			return nil
		}))
	}
	p.heldTheFiles = held
}

// requireDestroyed fails unless every handle the probe was given is destroyed.
func (p *credentialProbe) requireDestroyed(t *testing.T) {
	t.Helper()
	for _, creds := range p.seen {
		require.NotNil(t, creds.Secret)
		assert.False(t, creds.Secret.IsAlive(), "New destroyed the secret handle")
		if p.reads.key {
			require.NotNil(t, creds.Key)
			assert.False(t, creds.Key.IsAlive(), "New destroyed the key handle")
		}
	}
}

func TestCredentialFiles_IsZero(t *testing.T) {
	assert.True(t, CredentialFiles{}.IsZero())
	assert.False(t, CredentialFiles{KeyFile: "k"}.IsZero())
	assert.False(t, CredentialFiles{SecretFile: "s"}.IsZero())
}

func TestCredentialFiles_Validate(t *testing.T) {
	assert.NoError(t, CredentialFiles{}.Validate())
	assert.NoError(t, CredentialFiles{SecretFile: "s"}.Validate(), "a secret alone is an account key")
	assert.NoError(t, CredentialFiles{KeyFile: "k", SecretFile: "s"}.Validate())
	assert.ErrorIs(t, CredentialFiles{KeyFile: "k"}.Validate(), ErrCredentialKeyWithoutSecret)
}

func TestOpenCredentials_NoneLeavesTheDefaultChain(t *testing.T) {
	creds, release, err := openCredentials(CredentialFiles{})
	require.NoError(t, err)
	require.NotNil(t, release)
	release()
	assert.True(t, creds.IsZero())
}

func TestOpenCredentials_HandlesHoldTheFilesAndReleaseDestroysThem(t *testing.T) {
	creds, release, err := openCredentials(fakeFiles(t))
	require.NoError(t, err)

	ok, err := creds.Key.EqualTo([]byte(fakeKey))
	require.NoError(t, err)
	assert.True(t, ok, "key handle holds the key file without its line ending")
	ok, err = creds.Secret.EqualTo([]byte(fakeSecret))
	require.NoError(t, err)
	assert.True(t, ok, "secret handle holds the secret file without its line ending")

	release()
	assert.False(t, creds.Key.IsAlive(), "release destroys the key handle")
	assert.False(t, creds.Secret.IsAlive(), "release destroys the secret handle")
}

func TestOpenCredentials_SecretOnly(t *testing.T) {
	creds, release, err := openCredentials(CredentialFiles{SecretFile: writeCredentialFile(t, "secret", fakeSecret)})
	require.NoError(t, err)
	defer release()
	assert.Nil(t, creds.Key)
	require.NotNil(t, creds.Secret)
	assert.Equal(t, len(fakeSecret), creds.Secret.Size())
}

// TestOpenCredentials_OpenZeroizesItsCopies is the retention proof: the only
// plaintext copies are the ones go-objstore's Credentials.Open makes for its
// callback, and they are zero once it returns.
func TestOpenCredentials_OpenZeroizesItsCopies(t *testing.T) {
	creds, release, err := openCredentials(fakeFiles(t))
	require.NoError(t, err)
	defer release()

	var key, secret []byte
	require.NoError(t, creds.Open(func(k, s []byte) error {
		assert.Equal(t, fakeKey, string(k))
		assert.Equal(t, fakeSecret, string(s))
		key, secret = k, s // retained only to inspect them after the call
		return nil
	}))
	assert.Equal(t, make([]byte, len(fakeKey)), key, "key copy is zeroized after the call")
	assert.Equal(t, make([]byte, len(fakeSecret)), secret, "secret copy is zeroized after the call")
}

func TestOpenCredentials_FailsClosedNamingTheFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent")
	key := writeCredentialFile(t, "key", fakeKey)
	cases := map[string]struct {
		files      CredentialFiles
		credential Credential
		file       string
	}{
		"key missing":    {files: CredentialFiles{KeyFile: missing, SecretFile: missing}, credential: CredentialKey, file: missing},
		"secret missing": {files: CredentialFiles{KeyFile: key, SecretFile: missing}, credential: CredentialSecret, file: missing},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			creds, release, err := openCredentials(tc.files)
			require.NotNil(t, release)
			defer release()
			assert.True(t, creds.IsZero(), "no handle escapes a failed open")
			var fileErr *CredentialFileError
			require.ErrorAs(t, err, &fileErr)
			assert.Equal(t, tc.credential, fileErr.Credential)
			assert.Equal(t, tc.file, fileErr.File)
			assert.ErrorIs(t, err, fs.ErrNotExist)
			assert.Contains(t, err.Error(), tc.file)
		})
	}
}

func TestOpenCredentials_RefusesAWorldReadableFile(t *testing.T) {
	path := writeCredentialFile(t, "secret", fakeSecret)
	require.NoError(t, os.Chmod(path, 0o644)) // #nosec G302 -- world-readable is the fault this test injects: openCredentials must refuse a secret file in that mode
	_, release, err := openCredentials(CredentialFiles{SecretFile: path})
	defer release()
	var modeErr *secure.FileModeError
	require.ErrorAs(t, err, &modeErr)
	assert.NotContains(t, err.Error(), fakeSecret)
}

func TestOpenCredentials_KeyWithoutSecret(t *testing.T) {
	_, release, err := openCredentials(CredentialFiles{KeyFile: writeCredentialFile(t, "key", fakeKey)})
	require.NotNil(t, release)
	release()
	assert.ErrorIs(t, err, ErrCredentialKeyWithoutSecret)
}

func TestNew_DeliversCredentialHandlesToTheProvider(t *testing.T) {
	probe := registerCredentialProbe(t, credentialReads{key: true, secret: true})
	e, err := New(credProbeProvider, map[string]string{}, WithCredentialFiles(fakeFiles(t)))
	require.NoError(t, err)
	defer func() { _ = e.Close() }()

	require.Len(t, probe.seen, 1, "the provider was configured once")
	assert.True(t, probe.liveDuringConfigure, "the handles were live while the provider configured")
	assert.True(t, probe.heldTheFiles, "the handles held the referenced files during Configure")
	probe.requireDestroyed(t)

	require.NoError(t, e.Set([]byte("k"), []byte("v")))
	got, err := e.Get([]byte("k"))
	require.NoError(t, err)
	assert.Equal(t, []byte("v"), got)
}

func TestNew_DestroysCredentialHandlesWhenTheProviderFails(t *testing.T) {
	probe := registerCredentialProbe(t, credentialReads{key: true, secret: true})
	probe.fail = errProbeConfigure
	e, err := New(credProbeProvider, map[string]string{}, WithCredentialFiles(fakeFiles(t)))
	assert.Nil(t, e)
	require.ErrorIs(t, err, errProbeConfigure)

	require.Len(t, probe.seen, 1, "the provider was configured once")
	assert.True(t, probe.liveDuringConfigure, "the handles were live while the provider configured")
	assert.True(t, probe.heldTheFiles, "the handles held the referenced files during Configure")
	probe.requireDestroyed(t)
}

func TestNew_RefusesCredentialsTheProviderIgnores(t *testing.T) {
	// The referenced files do not exist: a refusal naming them, rather than a
	// *CredentialFileError, proves New refused before reading anything.
	dir := t.TempDir()
	key, secret := filepath.Join(dir, "key"), filepath.Join(dir, "secret")
	cases := map[string]struct {
		provider string
		files    CredentialFiles
		want     Credential
		file     string
	}{
		"memory key and secret": {provider: "memory", files: CredentialFiles{KeyFile: key, SecretFile: secret}, want: CredentialKey, file: key},
		"memory secret":         {provider: "memory", files: CredentialFiles{SecretFile: secret}, want: CredentialSecret, file: secret},
		"local secret":          {provider: "local", files: CredentialFiles{SecretFile: secret}, want: CredentialSecret, file: secret},
		"secret-only key":       {provider: credProbeProvider, files: CredentialFiles{KeyFile: key, SecretFile: secret}, want: CredentialKey, file: key},
	}
	probe := registerCredentialProbe(t, credentialReads{secret: true})
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			settings := map[string]string{"path": t.TempDir()}
			e, err := New(tc.provider, settings, WithCredentialFiles(tc.files))
			assert.Nil(t, e)
			var ignored *CredentialIgnoredError
			require.ErrorAs(t, err, &ignored)
			assert.Equal(t, tc.provider, ignored.Provider)
			assert.Equal(t, tc.want, ignored.Credential)
			assert.Equal(t, tc.file, ignored.File)
		})
	}
	assert.Empty(t, probe.seen, "a refused open never reaches the provider")
}

func TestProviderCredentials_MatchThePinnedProviders(t *testing.T) {
	assert.Equal(t, credentialReads{key: true, secret: true}, providerCredentials["s3"])
	assert.Equal(t, credentialReads{secret: true}, providerCredentials["azure"])
	for _, provider := range []string{"gcs", "local", "memory"} {
		_, listed := providerCredentials[provider]
		assert.False(t, listed, "%s reads no credential", provider)
	}
}

func TestCredentialIgnoredError(t *testing.T) {
	err := &CredentialIgnoredError{Provider: "gcs", Credential: CredentialSecret, File: "/etc/qrdb/secret"}
	msg := err.Error()
	for _, want := range []string{`"gcs"`, "secret", `"/etc/qrdb/secret"`} {
		assert.Contains(t, msg, want)
	}
}

func TestNew_CredentialFileFailureFailsTheOpen(t *testing.T) {
	probe := registerCredentialProbe(t, credentialReads{key: true, secret: true})
	missing := filepath.Join(t.TempDir(), "absent")
	e, err := New(credProbeProvider, map[string]string{}, WithCredentialFiles(CredentialFiles{SecretFile: missing}))
	assert.Nil(t, e)
	var fileErr *CredentialFileError
	require.ErrorAs(t, err, &fileErr)
	assert.Equal(t, missing, fileErr.File)

	e, err = New(credProbeProvider, map[string]string{}, WithCredentialFiles(CredentialFiles{KeyFile: missing}))
	assert.Nil(t, e)
	assert.ErrorIs(t, err, ErrCredentialKeyWithoutSecret)
	assert.Empty(t, probe.seen, "a failed credential read never reaches the provider")
}

func TestNew_RefusesACredentialInSettings(t *testing.T) {
	e, err := New("memory", map[string]string{"secret_key": fakeSecret})
	assert.Nil(t, e)
	var settingErr *objcommon.CredentialSettingError
	require.ErrorAs(t, err, &settingErr)
	assert.NotContains(t, err.Error(), fakeSecret)
}

func TestCredentialOpenError(t *testing.T) {
	files := CredentialFiles{KeyFile: "/run/key", SecretFile: "/run/secret"}
	unavailable := &objcommon.CredentialUnavailableError{Credential: "secret key", Err: errors.New("destroyed")}
	err := credentialOpenError(files, unavailable)
	var wrapped *CredentialUnavailableError
	require.ErrorAs(t, err, &wrapped)
	assert.Equal(t, files.KeyFile, wrapped.KeyFile)
	assert.Equal(t, files.SecretFile, wrapped.SecretFile)
	assert.ErrorIs(t, err, unavailable)
	assert.Contains(t, err.Error(), `"/run/key"`)
	assert.Contains(t, err.Error(), `"/run/secret"`)

	other := errors.New("other")
	assert.Same(t, other, credentialOpenError(files, other))
}

func TestCredentialFileError(t *testing.T) {
	cause := errors.New("cause")
	err := &CredentialFileError{Credential: CredentialSecret, File: "/run/secret", Err: cause}
	assert.ErrorIs(t, err, cause)
	assert.True(t, strings.HasPrefix(err.Error(), "objstore: secret file \"/run/secret\""), err.Error())
	assert.False(t, bytes.Contains([]byte(err.Error()), []byte(fakeSecret)))
}
