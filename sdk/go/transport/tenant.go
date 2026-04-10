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

package transport

import (
	"context"
	"strings"
)

// TenantKVClient wraps a KVClient with automatic key prefixing for
// multi-tenant isolation. All keys are transparently prefixed with
// "{tenantID}/" on write operations and stripped on read/list operations.
type TenantKVClient struct {
	inner    KVClient
	tenantID string
	prefix   string // cached: tenantID + "/"
}

// WithTenant creates a new TenantKVClient that prefixes all keys with
// the given tenant ID. The tenantID must not be empty or contain
// path separators.
func WithTenant(client KVClient, tenantID string) (*TenantKVClient, error) {
	if tenantID == "" {
		return nil, &ConfigError{Field: "tenantID", Reason: "must not be empty"}
	}
	if strings.Contains(tenantID, "/") || strings.Contains(tenantID, "\\") || strings.Contains(tenantID, "..") {
		return nil, &ConfigError{Field: "tenantID", Reason: "must not contain path separators or '..'"}
	}
	return &TenantKVClient{
		inner:    client,
		tenantID: tenantID,
		prefix:   tenantID + "/",
	}, nil
}

// TenantID returns the tenant ID used for key prefixing.
func (t *TenantKVClient) TenantID() string {
	return t.tenantID
}

// Put stores a key-value pair with the tenant prefix applied.
func (t *TenantKVClient) Put(ctx context.Context, key string, value []byte, opts ...WriteOption) error {
	return t.inner.Put(ctx, t.prefix+key, value, opts...)
}

// Get retrieves a value by key with the tenant prefix applied.
func (t *TenantKVClient) Get(ctx context.Context, key string, opts ...ReadOption) ([]byte, error) {
	return t.inner.Get(ctx, t.prefix+key, opts...)
}

// Delete removes a key with the tenant prefix applied.
func (t *TenantKVClient) Delete(ctx context.Context, key string, opts ...WriteOption) error {
	return t.inner.Delete(ctx, t.prefix+key, opts...)
}

// List returns keys matching the given prefix within the tenant namespace.
// Keys are returned with the tenant prefix stripped.
func (t *TenantKVClient) List(ctx context.Context, prefix string, opts ...ReadOption) ([]string, error) {
	keys, err := t.inner.List(ctx, t.prefix+prefix, opts...)
	if err != nil {
		return nil, err
	}
	stripped := make([]string, len(keys))
	for i, k := range keys {
		stripped[i] = strings.TrimPrefix(k, t.prefix)
	}
	return stripped, nil
}

// Scan returns all key-value pairs matching the given prefix within
// the tenant namespace. Keys in the result map have the tenant prefix
// stripped.
func (t *TenantKVClient) Scan(ctx context.Context, prefix string, opts ...ReadOption) (map[string][]byte, error) {
	pairs, err := t.inner.Scan(ctx, t.prefix+prefix, opts...)
	if err != nil {
		return nil, err
	}
	result := make(map[string][]byte, len(pairs))
	for k, v := range pairs {
		result[strings.TrimPrefix(k, t.prefix)] = v
	}
	return result, nil
}

// Exists checks whether a key exists within the tenant namespace.
func (t *TenantKVClient) Exists(ctx context.Context, key string, opts ...ReadOption) (bool, error) {
	return t.inner.Exists(ctx, t.prefix+key, opts...)
}

// Compile-time check: TenantKVClient implements KVClient.
var _ KVClient = (*TenantKVClient)(nil)
