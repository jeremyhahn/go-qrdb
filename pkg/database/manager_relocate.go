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

package database

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// RelocateTenantDataDirs rewrites the data directory of every record of
// tenantID's databases that lies under fromRoot to the same path under
// toRoot, and closes a database this Manager holds open from fromRoot so
// its next open uses the rewritten path. A record outside fromRoot, or with
// no data directory of its own, is left as it is.
func (m *Manager) RelocateTenantDataDirs(ctx context.Context, tenantID, fromRoot, toRoot string) error {
	if fromRoot == "" || toRoot == "" || filepath.Clean(fromRoot) == filepath.Clean(toRoot) {
		return nil
	}
	cat, err := m.catalog("relocate database")
	if err != nil {
		return err
	}
	metas, err := cat.ListTenant(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("database: list the databases of tenant %s: %w", tenantID, err)
	}
	for _, meta := range metas {
		moved, ok := relocatedPath(meta.DataDir, fromRoot, toRoot)
		if !ok {
			continue
		}
		if err := cat.Update(ctx, meta.TenantID, meta.Name, meta.Incarnation, func(rec *DatabaseMeta) error {
			if next, under := relocatedPath(rec.DataDir, fromRoot, toRoot); under {
				rec.DataDir = next
			}
			return nil
		}); err != nil {
			return fmt.Errorf("database: relocate %s of tenant %s to %s: %w", meta.Name, tenantID, moved, err)
		}
		key := keyFor(meta.TenantID, meta.Name)
		val, open := m.databases.Load(key)
		if !open {
			continue
		}
		db := val.(*Database) //nolint:errcheck // type assertion on a value this container only ever holds
		if _, under := relocatedPath(db.DataDir(), fromRoot, toRoot); !under {
			continue
		}
		if m.databases.CompareAndDelete(key, db) {
			if err := db.Close(); err != nil {
				return fmt.Errorf("database: close %s of tenant %s at its old root: %w", meta.Name, tenantID, err)
			}
		}
	}
	return nil
}

// relocatedPath returns dir rebased from fromRoot onto toRoot, and whether
// dir lies under fromRoot.
func relocatedPath(dir, fromRoot, toRoot string) (string, bool) {
	if dir == "" {
		return "", false
	}
	rel, err := filepath.Rel(filepath.Clean(fromRoot), filepath.Clean(dir))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.Join(toRoot, rel), true
}
