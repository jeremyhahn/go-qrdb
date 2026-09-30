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

package tenant

// PrefixKey prepends the tenant ID to a key: "tenantID/key".
// For the default tenant, keys are still prefixed for consistency.
func PrefixKey(tenantID, key string) string {
	return tenantID + "/" + key
}

// PrefixKeyBytes prepends the tenant ID to a byte key.
func PrefixKeyBytes(tenantID string, key []byte) []byte {
	prefix := []byte(tenantID + "/")
	result := make([]byte, len(prefix)+len(key))
	copy(result, prefix)
	copy(result[len(prefix):], key)
	return result
}

// StripPrefix removes the tenant prefix from a key.
// Returns the original key if no tenant prefix is found.
func StripPrefix(tenantID, key string) string {
	prefix := tenantID + "/"
	if len(key) > len(prefix) && key[:len(prefix)] == prefix {
		return key[len(prefix):]
	}
	return key
}

// StripPrefixBytes removes the tenant prefix from a byte key.
// Returns the original key if no tenant prefix is found.
func StripPrefixBytes(tenantID string, key []byte) []byte {
	prefix := []byte(tenantID + "/")
	if len(key) > len(prefix) {
		match := true
		for i := range prefix {
			// #nosec G602 -- len(key) > len(prefix) is checked above, and i ranges over prefix.
			if key[i] != prefix[i] {
				match = false
				break
			}
		}
		if match {
			return key[len(prefix):]
		}
	}
	return key
}

// TenantPrefix returns the prefix string for a tenant.
func TenantPrefix(tenantID string) string {
	return tenantID + "/"
}
