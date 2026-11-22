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
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/cockroachdb/pebble/v2/objstorage/remote"
	objcommon "github.com/jeremyhahn/go-objstore/pkg/common"
)

// Remote adapts a go-objstore common.Storage to Pebble's
// objstorage/remote.Storage, so Pebble can keep its shared SSTables in a
// bucket (object-storage-lsm, docs/decisions/object-storage-lsm.md).
//
// Every object lives under one prefix per instance (OS-3 A): Pebble's object
// name objName is stored as "<prefix>/<objName>". go-objstore has no ranged
// read (OS-6 A interim), so ReadObject fetches the whole object once and
// serves ReadAt from the fetched bytes.
//
// Remote is not wired into any engine yet.
type Remote struct {
	store  objcommon.Storage
	prefix string
}

var _ remote.Storage = (*Remote)(nil)

// NewRemote returns a Remote over store whose objects live under prefix. A nil
// store, or a prefix that is empty, starts with "/" or contains a NUL, is
// refused with an *InvalidRemoteError.
func NewRemote(store objcommon.Storage, prefix string) (*Remote, error) {
	switch {
	case store == nil:
		return nil, &InvalidRemoteError{Prefix: prefix, Reason: "nil store"}
	case prefix == "":
		return nil, &InvalidRemoteError{Prefix: prefix, Reason: "empty prefix"}
	case strings.HasPrefix(prefix, "/"):
		return nil, &InvalidRemoteError{Prefix: prefix, Reason: "prefix has a leading /"}
	case strings.ContainsRune(prefix, 0):
		return nil, &InvalidRemoteError{Prefix: prefix, Reason: "prefix contains a NUL"}
	}
	return &Remote{store: store, prefix: prefix}, nil
}

// key returns the store key of the Pebble object objName.
func (r *Remote) key(objName string) string {
	return r.prefix + "/" + objName
}

// ReadObject fetches the whole object and returns a reader over its bytes,
// with the object's size.
func (r *Remote) ReadObject(ctx context.Context, objName string) (remote.ObjectReader, int64, error) {
	rc, err := r.store.GetWithContext(ctx, r.key(objName))
	if err != nil {
		return nil, 0, mapNotExist(err)
	}
	data, err := io.ReadAll(rc)
	closeErr := rc.Close()
	if err != nil {
		return nil, 0, err
	}
	if closeErr != nil {
		return nil, 0, closeErr
	}
	return &remoteReader{data: data}, int64(len(data)), nil
}

// CreateObject returns a writer that buffers the object and stores it on
// Close.
func (r *Remote) CreateObject(objName string) (io.WriteCloser, error) {
	return &remoteWriter{store: r.store, key: r.key(objName)}, nil
}

// List returns the names, relative to this Remote's prefix, of the objects
// whose name starts with listPrefix. As in Pebble's own in-memory and local
// storage, the list prefix is kept in each name. When delimiter is non-empty,
// each name is cut after the first delimiter following listPrefix, and the
// results are deduplicated. The result is sorted.
func (r *Remote) List(listPrefix, delimiter string) ([]string, error) {
	keys, err := r.store.ListWithContext(context.Background(), r.key(listPrefix))
	if err != nil {
		return nil, err
	}
	base := r.prefix + "/"
	seen := make(map[string]struct{}, len(keys))
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		name, ok := strings.CutPrefix(k, base)
		if !ok || !strings.HasPrefix(name, listPrefix) {
			continue
		}
		if delimiter != "" {
			if i := strings.Index(name[len(listPrefix):], delimiter); i >= 0 {
				name = name[:len(listPrefix)+i+len(delimiter)]
			}
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

// Delete removes the named object.
func (r *Remote) Delete(objName string) error {
	return mapNotExist(r.store.DeleteWithContext(context.Background(), r.key(objName)))
}

// Size returns the size of the named object in bytes.
func (r *Remote) Size(objName string) (int64, error) {
	md, err := r.store.GetMetadata(context.Background(), r.key(objName))
	if err != nil {
		return 0, mapNotExist(err)
	}
	return md.Size, nil
}

// mapNotExist maps go-objstore's other missing-object error, the
// ErrMetadataNotFound a provider's GetMetadata returns, to one that also
// matches ErrKeyNotFound, so IsNotExistError reports every missing object.
func mapNotExist(err error) error {
	if err != nil && !errors.Is(err, objcommon.ErrKeyNotFound) && errors.Is(err, objcommon.ErrMetadataNotFound) {
		return fmt.Errorf("%w: %w", objcommon.ErrKeyNotFound, err)
	}
	return err
}

// IsNotExistError reports whether err says the object does not exist.
func (r *Remote) IsNotExistError(err error) bool {
	return errors.Is(err, objcommon.ErrKeyNotFound)
}

// Close returns nil. The store is shared and is not closed.
func (r *Remote) Close() error { return nil }

// remoteReader serves ReadAt from the bytes ReadObject fetched.
type remoteReader struct {
	mu   sync.RWMutex
	data []byte
}

// errReaderClosed is returned by ReadAt after Close.
var errReaderClosed = errors.New("objstore: remote reader is closed")

// errNegativeOffset is returned by ReadAt for an offset below zero.
var errNegativeOffset = errors.New("objstore: remote read at a negative offset")

// ReadAt copies len(p) bytes from offset off. A read that would pass the end
// of the object returns io.EOF and no partial result, as Pebble's in-memory
// storage does.
func (rr *remoteReader) ReadAt(_ context.Context, p []byte, off int64) error {
	rr.mu.RLock()
	defer rr.mu.RUnlock()
	if rr.data == nil {
		return errReaderClosed
	}
	if off < 0 {
		return errNegativeOffset
	}
	if off+int64(len(p)) > int64(len(rr.data)) {
		return io.EOF
	}
	copy(p, rr.data[off:])
	return nil
}

// Close releases the fetched bytes.
func (rr *remoteReader) Close() error {
	rr.mu.Lock()
	rr.data = nil
	rr.mu.Unlock()
	return nil
}

// errWriterClosed is returned by Write after Close.
var errWriterClosed = errors.New("objstore: write to a closed remote object writer")

// remoteWriter buffers an object and stores it on Close.
type remoteWriter struct {
	store  objcommon.Storage
	key    string
	buf    bytes.Buffer
	closed bool
}

// Write appends p to the buffered object.
func (w *remoteWriter) Write(p []byte) (int, error) {
	if w.closed {
		return 0, errWriterClosed
	}
	return w.buf.Write(p)
}

// Close stores the buffered object. A second Close is a no-op returning nil.
func (w *remoteWriter) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	err := w.store.PutWithContext(context.Background(), w.key, bytes.NewReader(w.buf.Bytes()))
	w.buf = bytes.Buffer{}
	return err
}
