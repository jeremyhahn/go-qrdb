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
	"errors"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	objcommon "github.com/jeremyhahn/go-objstore/pkg/common"
	objfactory "github.com/jeremyhahn/go-objstore/pkg/factory"
	objmemory "github.com/jeremyhahn/go-objstore/pkg/memory"
)

// memoryCreator opens go-objstore's memory provider and counts its calls.
func memoryCreator(calls *int) func(map[string]string, objcommon.Credentials) (objcommon.Storage, error) {
	return func(settings map[string]string, creds objcommon.Credentials) (objcommon.Storage, error) {
		*calls++
		store := objmemory.New()
		if err := store.Configure(settings, creds); err != nil {
			return nil, err
		}
		return store, nil
	}
}

func TestRegisterProvider_RefusesAnIncompleteRegistration(t *testing.T) {
	var calls int
	assert.ErrorIs(t, RegisterProvider("", ProviderCredentials{Secret: true}, memoryCreator(&calls)), ErrInvalidProviderRegistration)
	assert.ErrorIs(t, RegisterProvider("register-test-nil-creator", ProviderCredentials{}, nil), ErrInvalidProviderRegistration)
	assert.Equal(t, credentialReads{}, credentialReadsOf("register-test-nil-creator"), "a refused registration records nothing")
}

func TestRegisterProvider_RefusesANameTheFactoryServes(t *testing.T) {
	var calls int
	err := RegisterProvider("memory", ProviderCredentials{Key: true, Secret: true}, memoryCreator(&calls))
	var registered *ProviderRegisteredError
	require.ErrorAs(t, err, &registered)
	assert.Equal(t, "memory", registered.Provider)
	assert.Contains(t, err.Error(), `"memory"`)
	assert.Equal(t, credentialReads{}, credentialReadsOf("memory"), "go-objstore's memory provider still reads no credential")
}

func TestRegisterProvider_RefusesASecondRegistration(t *testing.T) {
	var first, second int
	require.NoError(t, RegisterProvider("register-test-twice", ProviderCredentials{Secret: true}, memoryCreator(&first)))
	var registered *ProviderRegisteredError
	require.ErrorAs(t, RegisterProvider("register-test-twice", ProviderCredentials{Key: true, Secret: true}, memoryCreator(&second)), &registered)
	assert.Equal(t, credentialReads{secret: true}, credentialReadsOf("register-test-twice"), "the first declaration stands")
}

func TestRegisterProvider_NewDeliversTheDeclaredCredentials(t *testing.T) {
	var calls int
	require.NoError(t, RegisterProvider("register-test-delivers", ProviderCredentials{Key: true, Secret: true}, memoryCreator(&calls)))
	e, err := New("register-test-delivers", map[string]string{}, WithCredentialFiles(fakeFiles(t)))
	require.NoError(t, err)
	defer func() { _ = e.Close() }()
	assert.Equal(t, 1, calls, "the registered creator opened the store")
}

func TestRegisterProvider_NewRefusesAnUndeclaredCredential(t *testing.T) {
	var calls int
	require.NoError(t, RegisterProvider("register-test-secret-only", ProviderCredentials{Secret: true}, memoryCreator(&calls)))
	key := filepath.Join(t.TempDir(), "key")
	_, err := New("register-test-secret-only", map[string]string{}, WithCredentialFiles(CredentialFiles{KeyFile: key, SecretFile: key}))
	var ignored *CredentialIgnoredError
	require.ErrorAs(t, err, &ignored)
	assert.Equal(t, CredentialKey, ignored.Credential)
	assert.Zero(t, calls, "a refused open never reaches the provider")
}

// TestRegisterProvider_ConcurrentWithRegistrationAndNew drives registrations
// of distinct names, contending registrations of one name and opens of
// registered and shipped providers at once. go-objstore's factory map is
// unsynchronized, so under -race this fails if any registration writes it.
func TestRegisterProvider_ConcurrentWithRegistrationAndNew(t *testing.T) {
	const workers = 16
	var (
		wg      sync.WaitGroup
		winners atomic.Int32
		start   = make(chan struct{})
		errs    = make(chan error, 4*workers)
	)
	for i := range workers {
		name := "register-test-concurrent-" + strconv.Itoa(i)
		wg.Add(4)
		go func() {
			defer wg.Done()
			<-start
			var calls int
			if err := RegisterProvider(name, ProviderCredentials{Secret: true}, memoryCreator(&calls)); err != nil {
				errs <- err
				return
			}
			e, err := New(name, map[string]string{})
			if err != nil {
				errs <- err
				return
			}
			errs <- e.Close()
		}()
		go func() {
			defer wg.Done()
			<-start
			var calls int
			if RegisterProvider("register-test-contended", ProviderCredentials{}, memoryCreator(&calls)) == nil {
				winners.Add(1)
			}
		}()
		go func() {
			defer wg.Done()
			<-start
			e, err := New("memory", map[string]string{})
			if err != nil {
				errs <- err
				return
			}
			errs <- e.Close()
		}()
		go func() {
			defer wg.Done()
			<-start
			_ = credentialReadsOf(name)
			_ = providerKnown(name)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	assert.Equal(t, int32(1), winners.Load(), "exactly one contending registration holds the name")
	for i := range workers {
		assert.True(t, providerKnown("register-test-concurrent-"+strconv.Itoa(i)))
	}
}

func TestRegisterProvider_RefusesAnArchiverName(t *testing.T) {
	archivers := objfactory.ListArchivers()
	require.NotEmpty(t, archivers, "go-objstore registers at least one archiver")
	var calls int
	for _, name := range archivers {
		var registered *ProviderRegisteredError
		require.ErrorAs(t, RegisterProvider(name, ProviderCredentials{Secret: true}, memoryCreator(&calls)), &registered, name)
	}
	assert.Zero(t, calls)
}

func TestResolveProvider(t *testing.T) {
	var calls int
	require.NoError(t, RegisterProvider("register-test-resolve", ProviderCredentials{Secret: true}, memoryCreator(&calls)))
	resolved, ok := resolveProvider("register-test-resolve")
	require.True(t, ok)
	assert.Equal(t, credentialReads{secret: true}, resolved.reads)
	store, err := resolved.creator(map[string]string{}, objcommon.Credentials{})
	require.NoError(t, err)
	assert.NotNil(t, store)
	assert.Equal(t, 1, calls, "a registered provider opens through its own creator")

	resolved, ok = resolveProvider("memory")
	require.True(t, ok)
	store, err = resolved.creator(map[string]string{}, objcommon.Credentials{})
	require.NoError(t, err)
	assert.NotNil(t, store, "a shipped provider opens through go-objstore's factory")

	resolved, ok = resolveProvider("register-test-absent")
	assert.False(t, ok)
	assert.Nil(t, resolved.creator, "an unknown provider resolves to no creator")
}

// credentialReadsOf returns the credential references provider resolves to.
func credentialReadsOf(provider string) credentialReads {
	resolved, _ := resolveProvider(provider)
	return resolved.reads
}

// providerKnown reports whether provider resolves.
func providerKnown(provider string) bool {
	_, ok := resolveProvider(provider)
	return ok
}

// TestNewRegistrationAfterResolve drives the registration race through the
// providerResolved seam: a provider registered after New resolved its name as
// unknown is never opened, so configured file credentials cannot reach a
// creator that declared it reads none and open the store under ambient
// credentials.
func TestNewRegistrationAfterResolve(t *testing.T) {
	const name = "register-test-race"
	var calls, hooks int
	providerResolved = func(provider string) {
		hooks++
		assert.Equal(t, name, provider)
		assert.NoError(t, RegisterProvider(name, ProviderCredentials{}, memoryCreator(&calls)))
	}
	t.Cleanup(func() { providerResolved = func(string) {} })

	_, err := New(name, map[string]string{}, WithCredentialFiles(fakeFiles(t)))
	var unsupported *UnsupportedProviderError
	require.ErrorAs(t, err, &unsupported)
	assert.Equal(t, name, unsupported.Provider)
	assert.Equal(t, 1, hooks, "the registration ran between resolution and creation")
	assert.Equal(t, 0, calls, "a provider registered after resolution is not opened")
}

// TestNewUnknownProviderReadsNoCredential proves an unknown provider is
// refused before a credential file is read: an absent file would otherwise
// surface as a CredentialFileError.
func TestNewUnknownProviderReadsNoCredential(t *testing.T) {
	absent := CredentialFiles{SecretFile: filepath.Join(t.TempDir(), "absent")}
	_, err := New("register-test-unknown-creds", map[string]string{}, WithCredentialFiles(absent))
	var unsupported *UnsupportedProviderError
	require.ErrorAs(t, err, &unsupported)
	var fileErr *CredentialFileError
	assert.False(t, errors.As(err, &fileErr), "no credential file is opened for an unknown provider")
}

func TestResolveProvider_ShippedReads(t *testing.T) {
	// A shipped provider resolves exactly when its build tag compiled it in,
	// and then reads its pinned entry; one left out resolves to nothing.
	for name, pinned := range map[string]credentialReads{
		"s3":     {key: true, secret: true},
		"azure":  {secret: true},
		"local":  {},
		"memory": {},
	} {
		resolved, ok := resolveProvider(name)
		compiled := objfactory.IsStorageBackendRegistered(name)
		assert.Equal(t, compiled, ok, name)
		want := credentialReads{}
		if compiled {
			want = pinned
		}
		assert.Equal(t, want, resolved.reads, name)
	}
	assert.Equal(t, credentialReads{}, credentialReadsOf("register-test-unknown"))
}

// TestNew_RefusesInlineCredentialsBeforeARegisteredCreator proves New
// refuses settings carrying a credential inline itself, so a registered
// creator that does not delegate to a go-objstore Configure never sees them.
func TestNew_RefusesInlineCredentialsBeforeARegisteredCreator(t *testing.T) {
	var calls int
	require.NoError(t, RegisterProvider("register-test-inline", ProviderCredentials{}, func(map[string]string, objcommon.Credentials) (objcommon.Storage, error) {
		calls++
		return objmemory.New(), nil
	}))
	for _, setting := range []string{"secret_key", "access_key"} {
		t.Run(setting, func(t *testing.T) {
			_, err := New("register-test-inline", map[string]string{setting: inlineCredentialValue})
			var settingErr *CredentialSettingError
			require.ErrorAs(t, err, &settingErr)
			assert.Equal(t, setting, settingErr.Setting)
			assert.NotContains(t, err.Error(), inlineCredentialValue)
		})
	}
	assert.Zero(t, calls, "the registered creator never received the inline credential")
}

// TestNew_RefusesInlineCredentialsForAShippedProvider proves the refusal
// also holds for a provider go-objstore ships, before any store is built.
func TestNew_RefusesInlineCredentialsForAShippedProvider(t *testing.T) {
	_, err := New("memory", map[string]string{"secret_key": inlineCredentialValue})
	var settingErr *CredentialSettingError
	require.ErrorAs(t, err, &settingErr)
	assert.Equal(t, "secret_key", settingErr.Setting)
}

// TestRegisterProvider_RefusesEveryShippedName proves every provider name
// go-objstore ships is reserved whether or not its build tag compiled it in,
// so a registration never takes a name whose pinned credential reads would
// override the ones it declared.
func TestRegisterProvider_RefusesEveryShippedName(t *testing.T) {
	for _, name := range []string{"s3", "azure", "gcs", "glacier", "azurearchive", "local", "memory"} {
		t.Run(name, func(t *testing.T) {
			var calls int
			err := RegisterProvider(name, ProviderCredentials{Key: true, Secret: true}, memoryCreator(&calls))
			var registered *ProviderRegisteredError
			require.ErrorAs(t, err, &registered)
			assert.Equal(t, name, registered.Provider)
			_, stored := registeredProviders.Load(name)
			assert.False(t, stored, "a shipped name is never held by a registration")
		})
	}
	assert.Equal(t, credentialReads{secret: true}, providerCredentials["azure"], "azure keeps its pinned reads")
	assert.Equal(t, credentialReads{key: true, secret: true}, providerCredentials["s3"], "s3 keeps its pinned reads")
}

// TestShippedProviders_CoversTheCompiledFactory fails when go-objstore
// compiles in a provider the pinned shippedProviders set does not reserve.
func TestShippedProviders_CoversTheCompiledFactory(t *testing.T) {
	for _, name := range append(objfactory.ListStorageBackends(), objfactory.ListArchivers()...) {
		_, shipped := shippedProviders[name]
		assert.True(t, shipped, "go-objstore provider %q is missing from shippedProviders", name)
	}
	for name := range providerCredentials {
		_, shipped := shippedProviders[name]
		assert.True(t, shipped, "pinned provider %q is missing from shippedProviders", name)
	}
}
