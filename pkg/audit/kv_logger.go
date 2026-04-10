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

package audit

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// kvLogger writes audit entries to a KVStore backend.
type kvLogger struct {
	store kvstore.KVStore
}

// Log serializes the entry as JSON and writes it to the KVStore.
// The key format is: __sys:audit/{tenantID}/{timestamp_nano}
func (l *kvLogger) Log(ctx context.Context, entry *Entry) error {
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	}

	key := "__sys:audit/" + entry.TenantID + "/" + strconv.FormatInt(entry.Timestamp.UnixNano(), 10)

	data, err := json.Marshal(entry)
	if err != nil {
		return &WriteError{Backend: "kv", Err: err}
	}

	if err := l.store.Put(ctx, key, data); err != nil {
		return &WriteError{Backend: "kv", Err: err}
	}
	return nil
}

// Close is a no-op for kvLogger because the KVStore lifecycle is
// managed externally.
func (l *kvLogger) Close() error { return nil }
