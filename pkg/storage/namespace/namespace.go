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

// Package namespace provides tenant isolation by prefixing all keys
// with a namespace separator.
package namespace

import (
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// Compile-time interface check.
var _ storage.Engine = (*Engine)(nil)

const separator = "/"

// Engine wraps another Engine and prefixes all keys with a namespace.
type Engine struct {
	inner     storage.Engine
	namespace string
	prefix    string // namespace + separator, cached
}

// New creates a namespaced engine that prefixes all keys with namespace + "/".
func New(inner storage.Engine, namespace string) *Engine {
	return &Engine{
		inner:     inner,
		namespace: namespace,
		prefix:    namespace + separator,
	}
}

// Namespace returns the namespace string.
func (e *Engine) Namespace() string {
	return e.namespace
}

// Get retrieves the value for the namespaced key.
func (e *Engine) Get(key string) ([]byte, error) {
	if key == "" {
		return nil, storage.ErrEmptyKey
	}
	return e.inner.Get(e.prefix + key)
}

// Put stores a key-value pair under the namespace prefix.
func (e *Engine) Put(key string, data []byte) error {
	if key == "" {
		return storage.ErrEmptyKey
	}
	return e.inner.Put(e.prefix+key, data)
}

// Delete removes a namespaced key.
func (e *Engine) Delete(key string) error {
	if key == "" {
		return storage.ErrEmptyKey
	}
	return e.inner.Delete(e.prefix + key)
}

// List returns all keys matching the given prefix within the namespace.
// The namespace prefix is stripped from the returned keys.
func (e *Engine) List(prefix string) ([]string, error) {
	keys, err := e.inner.List(e.prefix + prefix)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(keys))
	for _, k := range keys {
		result = append(result, strings.TrimPrefix(k, e.prefix))
	}
	return result, nil
}

// Exists reports whether the namespaced key exists.
func (e *Engine) Exists(key string) (bool, error) {
	if key == "" {
		return false, storage.ErrEmptyKey
	}
	return e.inner.Exists(e.prefix + key)
}

// Close releases resources held by the underlying engine.
func (e *Engine) Close() error {
	return e.inner.Close()
}
