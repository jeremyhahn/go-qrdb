// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
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

package clustermig

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// MaxCredentialFileSize bounds a migration credential file. A passphrase is
// far smaller; anything larger is not a credential file.
const MaxCredentialFileSize = 64 << 10

// systemCredentialFile names the node barrier's credential file; a tenant
// barrier's is tenantCredentialPrefix + the tenant ID.
const (
	systemCredentialFile   = "system"
	tenantCredentialPrefix = "tenant-"
)

// CredentialSource supplies this node's own unlock credential for a
// migration it owns. No credential travels in the replicated record or
// between nodes: each owner obtains its own locally.
type CredentialSource interface {
	// Credential returns this node's credential for m. The caller wipes it.
	// Any error means the credential is not available yet: the owner waits
	// in phase awaiting-credential and is asked again on the next pass.
	Credential(ctx context.Context, m *ClusterMigration) ([]byte, error)
	// Release drops whatever the source holds for m's generation once m no
	// longer needs it: its recycle completed or was abandoned.
	Release(m *ClusterMigration)
}

// LocalCredentials is the CredentialSource a node runs. The node that
// accepted the request holds the request's credential in protected memory
// for that migration's generation; every other owner -- and that node too,
// once the held credential is gone -- reads its own from a file in dir:
// "system" for the node barrier, "tenant-<id>" for a tenant barrier. The file
// must be a regular file owned by the daemon's user with no group or other
// permission bits; one trailing newline is not part of the credential.
type LocalCredentials struct {
	dir string
	// uid is the user that must own a credential file: the daemon's.
	uid int
	// held maps a barrier's scope to its *heldCredential.
	held sync.Map
}

// heldCredential is a request credential held for one generation.
type heldCredential struct {
	generation uint64
	creds      seal.Credentials
}

var _ CredentialSource = (*LocalCredentials)(nil)

// NewLocalCredentials returns the credential source for a node whose
// credential files live in dir. An empty dir means no files: an owner
// without a held credential waits.
func NewLocalCredentials(dir string) *LocalCredentials {
	return &LocalCredentials{dir: dir, uid: os.Getuid()}
}

// Hold keeps a copy of credential, in protected memory, as this node's
// credential for m's generation. It is how the node that accepted a request
// recycles with the request's own credential without writing it anywhere.
// A later generation for the same barrier replaces it.
func (l *LocalCredentials) Hold(ctx context.Context, m *ClusterMigration, credential []byte) error {
	if len(credential) == 0 {
		return ErrEmptyCredential
	}
	creds, err := seal.CredentialsFor(ctx, credential)
	if err != nil {
		return err
	}
	if prev, loaded := l.held.Swap(scopeLabel(m.TenantID, m.Database), &heldCredential{generation: m.Generation, creds: creds}); loaded {
		prev.(*heldCredential).creds.Secret.Destroy() //nolint:errcheck // type assertion on a value this map only ever holds
	}
	return nil
}

// Release destroys the credential held for m's generation, if any. A newer
// generation's credential is left alone.
func (l *LocalCredentials) Release(m *ClusterMigration) {
	key := scopeLabel(m.TenantID, m.Database)
	v, ok := l.held.Load(key)
	if !ok {
		return
	}
	h := v.(*heldCredential) //nolint:errcheck // type assertion on a value this map only ever holds
	if h.generation != m.Generation {
		return
	}
	if l.held.CompareAndDelete(key, h) {
		h.creds.Secret.Destroy()
	}
}

// Credential returns this node's credential for m: the held request
// credential for m's generation, or else the contents of this node's
// credential file. ErrCredentialAwaited means neither is there yet;
// *MigrationCredentialFileError means the file is there but refused.
func (l *LocalCredentials) Credential(ctx context.Context, m *ClusterMigration) ([]byte, error) {
	if v, ok := l.held.Load(scopeLabel(m.TenantID, m.Database)); ok {
		h := v.(*heldCredential) //nolint:errcheck // type assertion on a value this map only ever holds
		if h.generation == m.Generation {
			return openCopy(ctx, h.creds)
		}
	}
	if l.dir == "" {
		return nil, ErrCredentialAwaited
	}
	name, err := credentialFileName(m.TenantID)
	if err != nil {
		return nil, &MigrationCredentialFileError{Path: filepath.Join(l.dir, tenantCredentialPrefix+m.TenantID), Reason: err.Error()}
	}
	return l.readFile(name)
}

// openCopy returns a copy of creds' secret that the caller owns and wipes.
func openCopy(ctx context.Context, creds seal.Credentials) ([]byte, error) {
	opened, err := creds.Secret.Open(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if derr := opened.Destroy(); derr != nil {
			slog.Warn("clustermig: releasing an opened credential enclave reported a non-fatal problem",
				slog.String("error", derr.Error()))
		}
	}()
	var out []byte
	err = opened.WithBytes(func(b []byte) error {
		out = bytes.Clone(b)
		return nil
	})
	return out, err
}

// errNotAFileName is why a tenant ID that is not a plain file name is
// refused: it could name a file outside the credentials directory.
var errNotAFileName = errors.New("tenant id is not a plain file name")

// credentialFileName names the credential file for tenantID's barrier, or
// the node barrier's for an empty tenantID.
func credentialFileName(tenantID string) (string, error) {
	if tenantID == "" {
		return systemCredentialFile, nil
	}
	if tenantID == "." || tenantID == ".." || strings.ContainsAny(tenantID, "/\\\x00") {
		return "", errNotAFileName
	}
	return tenantCredentialPrefix + tenantID, nil
}

// readFile reads the credential file name. The credentials directory must be
// a real directory the daemon's user owns and no one else can write --
// otherwise someone else could replace the file. The directory is opened once,
// without following a symlink, and vetted on that descriptor; the file is
// opened relative to it, so a directory swapped in after the vetting is never
// read. The file is opened without following a symlink and without blocking
// on a FIFO, and checked on its descriptor: a regular file the daemon's user
// owns and no one else can read.
func (l *LocalCredentials) readFile(name string) ([]byte, error) {
	dir, err := l.openDir()
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	return l.readAt(dir, name)
}

// openDir opens and vets the credentials directory: not a symlink, a
// directory, owned by the daemon's user and writable by no one else. The
// returned descriptor is the directory every file is then opened in.
func (l *LocalCredentials) openDir() (*os.File, error) {
	dir, err := openCredentialDir(l.dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrCredentialAwaited
		}
		return nil, &MigrationCredentialFileError{Path: l.dir, Reason: err.Error()}
	}
	fi, err := dir.Stat()
	if err != nil {
		_ = dir.Close()
		return nil, &MigrationCredentialFileError{Path: l.dir, Reason: err.Error()}
	}
	if reason := l.unsafeDir(fi); reason != "" {
		_ = dir.Close()
		return nil, &MigrationCredentialFileError{Path: l.dir, Reason: reason}
	}
	return dir, nil
}

// readAt reads and vets the credential file name inside the vetted directory
// dir.
func (l *LocalCredentials) readAt(dir *os.File, name string) ([]byte, error) {
	path := filepath.Join(l.dir, name)
	f, err := openCredentialAt(dir, name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrCredentialAwaited
		}
		return nil, &MigrationCredentialFileError{Path: path, Reason: err.Error()}
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, &MigrationCredentialFileError{Path: path, Reason: err.Error()}
	}
	if reason := l.unsafe(fi); reason != "" {
		return nil, &MigrationCredentialFileError{Path: path, Reason: reason}
	}
	buf := make([]byte, fi.Size())
	if _, err := io.ReadFull(f, buf); err != nil {
		clear(buf)
		return nil, &MigrationCredentialFileError{Path: path, Reason: err.Error()}
	}
	cred := trimNewline(buf)
	// The trimmed bytes stay in the buffer the caller holds; wipe them.
	clear(buf[len(cred):])
	if len(cred) == 0 {
		return nil, &MigrationCredentialFileError{Path: path, Reason: "empty"}
	}
	return cred, nil
}

// unsafeDir returns why the opened credentials directory fi may not hold
// credentials, or "" when it may.
func (l *LocalCredentials) unsafeDir(fi fs.FileInfo) string {
	switch {
	case !fi.IsDir():
		return "not a directory (" + fi.Mode().Type().String() + ")"
	case fi.Mode().Perm()&0o022 != 0:
		return "mode " + strconv.FormatUint(uint64(fi.Mode().Perm()), 8) + " lets group or others write; the directory must be writable by the daemon's user only"
	}
	uid, ok := fileOwner(fi)
	if !ok {
		return "its owner cannot be determined on this platform"
	}
	if uid != l.uid {
		return "owned by uid " + strconv.Itoa(uid) + ", not the daemon's uid " + strconv.Itoa(l.uid)
	}
	return ""
}

// unsafe returns why fi may not hold a credential, or "" when it may.
func (l *LocalCredentials) unsafe(fi fs.FileInfo) string {
	switch {
	case !fi.Mode().IsRegular():
		return "not a regular file"
	case fi.Mode().Perm()&0o077 != 0:
		return "mode " + strconv.FormatUint(uint64(fi.Mode().Perm()), 8) + " grants group or other access; use 0600 or stricter"
	case fi.Size() > MaxCredentialFileSize:
		return "larger than " + strconv.Itoa(MaxCredentialFileSize) + " bytes"
	}
	uid, ok := fileOwner(fi)
	if !ok {
		return "its owner cannot be determined on this platform"
	}
	if uid != l.uid {
		return "owned by uid " + strconv.Itoa(uid) + ", not the daemon's uid " + strconv.Itoa(l.uid)
	}
	return ""
}

// trimNewline drops one trailing "\n" or "\r\n".
func trimNewline(b []byte) []byte {
	b = bytes.TrimSuffix(b, []byte("\n"))
	return bytes.TrimSuffix(b, []byte("\r"))
}
