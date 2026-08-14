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

package main

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/logger"
)

// KVBinding exposes key-value operations to the Wails frontend.
type KVBinding struct {
	logger *logger.Logger
	conn   *ConnectionBinding
}

// NewKVBinding creates a new KVBinding backed by the given connection.
func NewKVBinding(log *logger.Logger, conn *ConnectionBinding) *KVBinding {
	return &KVBinding{logger: log, conn: conn}
}

// Put stores a key-value pair through Raft consensus.
func (b *KVBinding) Put(key string, value string) error {
	b.logger.Debug("Put called", "key", key)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Put failed", "error", err)
		return err
	}
	if err := cl.KV().Put(context.Background(), key, []byte(value)); err != nil {
		b.logger.Error("Put failed", "key", key, "error", err)
		return err
	}
	b.logger.Debug("Put completed", "key", key)
	return nil
}

// Get retrieves the value for a key and returns it as a string.
func (b *KVBinding) Get(key string) (string, error) {
	b.logger.Debug("Get called", "key", key)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Get failed", "error", err)
		return "", err
	}
	val, err := cl.KV().Get(context.Background(), key)
	if err != nil {
		b.logger.Error("Get failed", "key", key, "error", err)
		return "", err
	}
	b.logger.Debug("Get completed", "key", key)
	return string(val), nil
}

// Delete removes a key through Raft consensus.
func (b *KVBinding) Delete(key string) error {
	b.logger.Debug("Delete called", "key", key)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Delete failed", "error", err)
		return err
	}
	if err := cl.KV().Delete(context.Background(), key); err != nil {
		b.logger.Error("Delete failed", "key", key, "error", err)
		return err
	}
	b.logger.Debug("Delete completed", "key", key)
	return nil
}

// List returns sorted keys matching the given prefix.
func (b *KVBinding) List(prefix string) ([]string, error) {
	b.logger.Debug("List called", "prefix", prefix)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("List failed", "error", err)
		return nil, err
	}
	keys, err := cl.KV().List(context.Background(), prefix)
	if err != nil {
		b.logger.Error("List failed", "prefix", prefix, "error", err)
		return nil, err
	}
	b.logger.Debug("List completed", "prefix", prefix, "count", len(keys))
	return keys, nil
}

// Exists returns true when the key exists in the store.
func (b *KVBinding) Exists(key string) (bool, error) {
	b.logger.Debug("Exists called", "key", key)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Exists failed", "error", err)
		return false, err
	}
	_, err = cl.KV().Get(context.Background(), key)
	if err != nil {
		b.logger.Debug("Exists completed", "key", key, "exists", false)
		return false, nil
	}
	b.logger.Debug("Exists completed", "key", key, "exists", true)
	return true, nil
}

// Scan returns all key-value pairs matching the given prefix as a
// string-to-string map suitable for JS interop.
func (b *KVBinding) Scan(prefix string) (map[string]string, error) {
	b.logger.Debug("Scan called", "prefix", prefix)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Scan failed", "error", err)
		return nil, err
	}
	raw, err := cl.KV().Scan(context.Background(), prefix)
	if err != nil {
		b.logger.Error("Scan failed", "prefix", prefix, "error", err)
		return nil, err
	}
	result := make(map[string]string, len(raw))
	for k, v := range raw {
		result[k] = string(v)
	}
	b.logger.Debug("Scan completed", "prefix", prefix, "count", len(result))
	return result, nil
}
