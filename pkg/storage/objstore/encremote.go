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
	"context"
	"io"

	"github.com/cockroachdb/pebble/v2/objstorage/remote"

	"github.com/jeremyhahn/go-qrdb/pkg/seal/encfs"
)

// ObjectBarrier is the key-encryption key EncryptedRemote wraps each
// object's data key with: the storage barrier (seal.StorageBarrier). It is
// declared here because pkg/seal's tests import this package.
type ObjectBarrier interface {
	Encrypt(plaintext []byte) ([]byte, error)
	Decrypt(ciphertext []byte) ([]byte, error)
}

// EncryptedRemote is a remote.Storage that seals every object with the
// barrier before it reaches the inner storage (object-storage-lsm OS-4 C).
// Each object is stored in the encfs object form (encfs.SealObject): the
// QENC header with a barrier-wrapped per-object DEK, then AES-256-GCM
// blocks of 4096 plaintext bytes whose additional data binds the object
// name, the block index and the final block, so a later ranged read can
// decrypt only the blocks it covers.
//
// It fails closed: a stored object that is not in that form is refused,
// never passed through. Barrier errors (a sealed barrier's seal.ErrSealed)
// are returned unchanged.
//
// EncryptedRemote is not wired into any engine yet.
type EncryptedRemote struct {
	inner   remote.Storage
	barrier ObjectBarrier
}

var _ remote.Storage = (*EncryptedRemote)(nil)

// NewEncryptedRemote returns an EncryptedRemote over inner sealing with
// barrier. A nil inner or barrier is refused with an
// *InvalidEncryptedRemoteError.
func NewEncryptedRemote(inner remote.Storage, barrier ObjectBarrier) (*EncryptedRemote, error) {
	switch {
	case inner == nil:
		return nil, &InvalidEncryptedRemoteError{Reason: "nil inner remote storage"}
	case barrier == nil:
		return nil, &InvalidEncryptedRemoteError{Reason: "nil barrier"}
	}
	return &EncryptedRemote{inner: inner, barrier: barrier}, nil
}

// readInner fetches length bytes from offset 0 of the inner object
// objName, and the inner object's size. length < 0 reads it whole.
func (e *EncryptedRemote) readInner(ctx context.Context, objName string, length int64) ([]byte, int64, error) {
	rd, size, err := e.inner.ReadObject(ctx, objName)
	if err != nil {
		return nil, 0, err
	}
	if length < 0 || length > size {
		length = size
	}
	buf := make([]byte, length)
	err = rd.ReadAt(ctx, buf, 0)
	closeErr := rd.Close()
	if err != nil {
		return nil, 0, err
	}
	if closeErr != nil {
		return nil, 0, closeErr
	}
	return buf, size, nil
}

// ReadObject fetches the whole sealed object, authenticates and decrypts it,
// and returns a reader over the plaintext with the plaintext size.
func (e *EncryptedRemote) ReadObject(ctx context.Context, objName string) (remote.ObjectReader, int64, error) {
	sealed, _, err := e.readInner(ctx, objName, -1)
	if err != nil {
		return nil, 0, err
	}
	plain, err := encfs.OpenObject(e.barrier, objName, sealed)
	if err != nil {
		return nil, 0, err
	}
	if plain == nil {
		plain = []byte{}
	}
	return &remoteReader{data: plain}, int64(len(plain)), nil
}

// CreateObject returns a writer that buffers the plaintext and, on Close,
// seals it and stores it through the inner storage.
func (e *EncryptedRemote) CreateObject(objName string) (io.WriteCloser, error) {
	return &encryptedWriter{remote: e, objName: objName}, nil
}

// List delegates to the inner storage; object names are not encrypted.
func (e *EncryptedRemote) List(prefix, delimiter string) ([]string, error) {
	return e.inner.List(prefix, delimiter)
}

// Delete delegates to the inner storage.
func (e *EncryptedRemote) Delete(objName string) error {
	return e.inner.Delete(objName)
}

// Size returns the plaintext size of the named object, from its header and
// its stored size; the object is not authenticated.
func (e *EncryptedRemote) Size(objName string) (int64, error) {
	hdr, size, err := e.readInner(context.Background(), objName, 64)
	if err != nil {
		return 0, err
	}
	return encfs.ObjectLogicalSize(hdr, size)
}

// IsNotExistError delegates to the inner storage.
func (e *EncryptedRemote) IsNotExistError(err error) bool {
	return e.inner.IsNotExistError(err)
}

// Close closes the inner storage.
func (e *EncryptedRemote) Close() error {
	return e.inner.Close()
}

// encryptedWriter buffers an object's plaintext and seals it on Close.
type encryptedWriter struct {
	remote  *EncryptedRemote
	objName string
	buf     bytes.Buffer
	closed  bool
}

// Write appends p to the buffered plaintext.
func (w *encryptedWriter) Write(p []byte) (int, error) {
	if w.closed {
		return 0, errWriterClosed
	}
	return w.buf.Write(p)
}

// Close seals the buffered plaintext and stores it. A second Close is a
// no-op returning nil.
func (w *encryptedWriter) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	plain := w.buf.Bytes()
	w.buf = bytes.Buffer{}
	sealed, err := encfs.SealObject(w.remote.barrier, w.objName, plain)
	if err != nil {
		return err
	}
	iw, err := w.remote.inner.CreateObject(w.objName)
	if err != nil {
		return err
	}
	if _, err := iw.Write(sealed); err != nil {
		_ = iw.Close()
		return err
	}
	return iw.Close()
}
