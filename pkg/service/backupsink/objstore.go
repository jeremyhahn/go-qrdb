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

// Package backupsink provides durable BackupSink implementations for the
// backup service. The Sink here persists backups to any storage.Storage —
// most usefully the object-store backend (local, s3, gcs, azure, minio) — so
// backups survive process restarts instead of being pinned in memory.
//
// Layout within the store:
//
//	meta/<id>  JSON-encoded BackupInfo (cheap to list/read)
//	data/<id>  gob-encoded map[shardID][]byte (the shard payloads)
//
// Keeping metadata separate from payloads lets List and ReadInfo avoid loading
// the dataset.
package backupsink

import (
	"bytes"
	"context"
	"encoding/gob"
	"encoding/json"
	"sort"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

const (
	metaPrefix = "meta/"
	dataPrefix = "data/"
)

// Sink persists backups to a storage.Storage. It satisfies service.BackupSink.
type Sink struct {
	store storage.Storage
}

// Compile-time interface assertion.
var _ service.BackupSink = (*Sink)(nil)

// New returns a Sink that persists backups to the given store. The store is
// typically a go-objstore-backed engine (pkg/storage/objstore), optionally
// namespaced with a prefix backend to isolate one repository per destination.
func New(store storage.Storage) *Sink {
	return &Sink{store: store}
}

// Write persists a backup's metadata and shard payloads.
func (s *Sink) Write(_ context.Context, info service.BackupInfo, shards map[uint64][]byte) error {
	metaBytes, err := json.Marshal(info)
	if err != nil {
		return &EncodeError{ID: info.ID, Err: err}
	}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(shards); err != nil {
		return &EncodeError{ID: info.ID, Err: err}
	}
	if err := s.store.Set([]byte(dataPrefix+info.ID), buf.Bytes()); err != nil {
		return err
	}
	// Write metadata last so a listed backup always has readable payloads.
	if err := s.store.Set([]byte(metaPrefix+info.ID), metaBytes); err != nil {
		// Best-effort rollback of the orphaned payload.
		_ = s.store.Delete([]byte(dataPrefix + info.ID))
		return err
	}
	return nil
}

// ReadInfo returns a backup's metadata without loading its payloads.
func (s *Sink) ReadInfo(_ context.Context, id string) (*service.BackupInfo, error) {
	raw, err := s.store.Get([]byte(metaPrefix + id))
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, &service.BackupNotFoundError{ID: id}
	}
	var info service.BackupInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return nil, &DecodeError{ID: id, Err: err}
	}
	return &info, nil
}

// ReadShards loads a backup's shard payloads.
func (s *Sink) ReadShards(_ context.Context, id string) (map[uint64][]byte, error) {
	raw, err := s.store.Get([]byte(dataPrefix + id))
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, &service.BackupNotFoundError{ID: id}
	}
	var shards map[uint64][]byte
	if err := gob.NewDecoder(bytes.NewReader(raw)).Decode(&shards); err != nil {
		return nil, &DecodeError{ID: id, Err: err}
	}
	return shards, nil
}

// List returns metadata for all persisted backups, newest first.
func (s *Sink) List(ctx context.Context) ([]service.BackupInfo, error) {
	keys, err := s.store.List([]byte(metaPrefix))
	if err != nil {
		return nil, err
	}
	result := make([]service.BackupInfo, 0, len(keys))
	for _, k := range keys {
		id := strings.TrimPrefix(string(k), metaPrefix)
		info, err := s.ReadInfo(ctx, id)
		if err != nil {
			return nil, err
		}
		result = append(result, *info)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Timestamp.After(result[j].Timestamp)
	})
	return result, nil
}

// Delete removes a persisted backup's metadata and payloads.
func (s *Sink) Delete(_ context.Context, id string) error {
	// Delete metadata first so a partially-deleted backup is not listed.
	if err := s.store.Delete([]byte(metaPrefix + id)); err != nil {
		return err
	}
	return s.store.Delete([]byte(dataPrefix + id))
}
