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
	"strconv"

	"github.com/jeremyhahn/go-enclave"
	objcommon "github.com/jeremyhahn/go-objstore/pkg/common"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// CredentialFiles references the provider credential by file, never by
// value. KeyFile holds the access key identifier and SecretFile the secret
// access key; a provider whose only secret is one key (an Azure storage
// account key) takes SecretFile alone. Both empty means no credential: the
// provider authenticates through its SDK's default chain. Each file must be
// owned by the process user and accessible by nobody else
// (secure.ReadFileHandle).
type CredentialFiles struct {
	KeyFile    string
	SecretFile string
}

// IsZero reports whether neither file is referenced.
func (f CredentialFiles) IsZero() bool { return f.KeyFile == "" && f.SecretFile == "" }

// Validate refuses a key without a secret: no provider authenticates with an
// access key identifier alone.
func (f CredentialFiles) Validate() error {
	if f.KeyFile != "" && f.SecretFile == "" {
		return ErrCredentialKeyWithoutSecret
	}
	return nil
}

// credentialReads records which credential references a provider's
// Configure reads.
type credentialReads struct {
	key, secret bool
}

// providerCredentials is, per go-objstore provider, what its pinned
// (v0.7.0-alpha) Configure reads of the Credentials New passes. A provider
// absent from it reads none: "gcs" builds its client from Google application
// default credentials, and "local" and "memory" need none. "azure" reads the
// secret as the storage account key and never the key.
var providerCredentials = map[string]credentialReads{
	"s3":    {key: true, secret: true},
	"azure": {secret: true},
}

// checkCredentialUse refuses a credential reference the provider would
// ignore: accepting it would open the store under the provider's ambient
// identity while the operator believes the configured credential is in use.
// It runs before any file is read, against the reads of the provider New
// resolved and will open.
func checkCredentialUse(provider string, reads credentialReads, files CredentialFiles) error {
	if files.KeyFile != "" && !reads.key {
		return &CredentialIgnoredError{Provider: provider, Credential: CredentialKey, File: files.KeyFile}
	}
	if files.SecretFile != "" && !reads.secret {
		return &CredentialIgnoredError{Provider: provider, Credential: CredentialSecret, File: files.SecretFile}
	}
	return nil
}

// Option configures New.
type Option func(*options)

type options struct {
	credentials CredentialFiles
	prefix      string
}

// WithCredentialFiles has New authenticate the provider with the credential
// the files reference. New reads each file into a protected-memory handle,
// hands the handles to go-objstore, which copies the bytes out only for the
// call that builds the vendor SDK credential, and destroys them before it
// returns: no copy of the credential is held by this package, by the Engine,
// or past the open.
func WithCredentialFiles(files CredentialFiles) Option {
	return func(o *options) { o.credentials = files }
}

// readFileHandle reads a credential file into a protected-memory handle. It
// is a variable so a test can observe each handle openCredentials opens,
// including one a later failure must destroy.
var readFileHandle = secure.ReadFileHandle

// openCredentials reads the referenced files into handles. The returned
// release destroys them and is never nil, also on error; a file that cannot
// be read fails closed with a *CredentialFileError naming it.
func openCredentials(files CredentialFiles) (objcommon.Credentials, func(), error) {
	var creds objcommon.Credentials
	release := func() {
		destroyHandle(creds.Key)
		destroyHandle(creds.Secret)
	}
	if err := files.Validate(); err != nil {
		return objcommon.Credentials{}, release, err
	}
	if files.KeyFile != "" {
		h, err := readFileHandle(files.KeyFile)
		if err != nil {
			return objcommon.Credentials{}, release, &CredentialFileError{Credential: CredentialKey, File: files.KeyFile, Err: err}
		}
		creds.Key = h
	}
	if files.SecretFile != "" {
		h, err := readFileHandle(files.SecretFile)
		if err != nil {
			return objcommon.Credentials{}, release, &CredentialFileError{Credential: CredentialSecret, File: files.SecretFile, Err: err}
		}
		creds.Secret = h
	}
	return creds, release, nil
}

// destroyHandle destroys h when it is set.
func destroyHandle(h enclave.ByteAccess) {
	if h != nil {
		secure.LogRelease("objstore credential handle", h.Destroy())
	}
}

// credentialOpenError names the referenced files when go-objstore reports a
// supplied handle it could not read; any other error is returned unchanged.
func credentialOpenError(files CredentialFiles, err error) error {
	var unavailable *objcommon.CredentialUnavailableError
	if errors.As(err, &unavailable) {
		return &CredentialUnavailableError{KeyFile: files.KeyFile, SecretFile: files.SecretFile, Err: err}
	}
	return err
}

// Credential names the half of a credential a CredentialFileError is about.
type Credential string

const (
	// CredentialKey is the access key identifier (CredentialFiles.KeyFile).
	CredentialKey Credential = "access key"

	// CredentialSecret is the secret access key (CredentialFiles.SecretFile).
	CredentialSecret Credential = "secret"
)

// ErrCredentialKeyWithoutSecret reports a CredentialFiles naming a key file
// and no secret file.
var ErrCredentialKeyWithoutSecret = errors.New("objstore: a credential key file needs a secret file")

// CredentialFileError reports a referenced credential file that could not be
// read into a protected-memory handle. It names the file, never its contents;
// Err is the *secure.FileHandleError, *secure.FileModeError or
// *secure.FileOwnerError that refused it.
type CredentialFileError struct {
	Credential Credential
	File       string
	Err        error
}

// Error implements the error interface.
func (e *CredentialFileError) Error() string {
	return "objstore: " + string(e.Credential) + " file " + strconv.Quote(e.File) + ": " + e.Err.Error()
}

// Unwrap returns the cause.
func (e *CredentialFileError) Unwrap() error { return e.Err }

// CredentialIgnoredError reports a credential file referenced for a provider
// whose go-objstore backend does not read that credential. New refuses it
// before reading the file rather than open the store under the provider's
// ambient identity. It names the file, never its contents.
type CredentialIgnoredError struct {
	Provider   string
	Credential Credential
	File       string
}

// Error implements the error interface.
func (e *CredentialIgnoredError) Error() string {
	return "objstore: provider " + strconv.Quote(e.Provider) + " does not read the " + string(e.Credential) +
		" credential; refusing the reference to file " + strconv.Quote(e.File)
}

// CredentialUnavailableError reports that go-objstore could not read a
// credential handle New supplied, so the open failed closed rather than fall
// back to the SDK default chain. It names the referenced files, never the
// credential; Err is go-objstore's *common.CredentialUnavailableError.
type CredentialUnavailableError struct {
	KeyFile    string
	SecretFile string
	Err        error
}

// Error implements the error interface.
func (e *CredentialUnavailableError) Error() string {
	return "objstore: credential from key file " + strconv.Quote(e.KeyFile) +
		" and secret file " + strconv.Quote(e.SecretFile) + " is unavailable: " + e.Err.Error()
}

// Unwrap returns the cause.
func (e *CredentialUnavailableError) Unwrap() error { return e.Err }

// CredentialSettingError reports a provider credential written inline into
// the provider settings instead of referenced by file through
// CredentialFiles. It names the setting, never its value; Err is
// go-objstore's *common.CredentialSettingError.
type CredentialSettingError struct {
	Setting string
	Err     error
}

// Error implements the error interface.
func (e *CredentialSettingError) Error() string {
	return "objstore: setting " + strconv.Quote(e.Setting) + " carries a credential inline; reference it by file"
}

// Unwrap returns the cause.
func (e *CredentialSettingError) Unwrap() error { return e.Err }

// RejectCredentialSettings refuses provider settings that carry a credential
// inline, which a configuration file would otherwise hold in plain text.
// go-objstore owns the set of credential setting names, so a provider it
// adds is refused here without a change to this package. It returns nil for
// settings that carry none, and otherwise a *CredentialSettingError naming
// the offending setting and never its value.
func RejectCredentialSettings(settings map[string]string) error {
	err := objcommon.RejectCredentialSettings(settings)
	if err == nil {
		return nil
	}
	settingErr := &CredentialSettingError{Err: err}
	var cause *objcommon.CredentialSettingError
	if errors.As(err, &cause) {
		settingErr.Setting = cause.Setting
	}
	return settingErr
}
