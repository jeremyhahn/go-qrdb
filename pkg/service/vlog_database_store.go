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

//go:build qrdb_vlog

package service

import (
	"context"
	"errors"
	"sync"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/feature"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

// databaseLogStore serves the verifiable logs of one barrier-enabled
// database from the database's own store: db.Backend() is the barrier
// backend, so every record is encrypted under the database's key and every
// call while it is sealed is refused. Commands are applied by
// statemachine.ApplyVerifiableLog over a vlog.Chain restored from that
// store, exactly as the engine applies them; refusals commit their staged
// writes as the engine does.
//
// mu serializes every propose and read. logs is nil until restored, and is
// set back to nil on any apply or commit failure and whenever the database
// is seen sealed, so no head state outlives a seal.
type databaseLogStore struct {
	db       *database.Database
	tenantID string
	name     string

	mu     sync.Mutex
	logs   *vlog.Chain
	served <-chan struct{}
}

// newDatabaseLogStore returns the store of tenantID's database db.
func newDatabaseLogStore(tenantID string, db *database.Database) *databaseLogStore {
	return &databaseLogStore{db: db, tenantID: tenantID, name: db.Name()}
}

// sealed reports whether the database is sealed, or stopped serving since
// the chain was restored (a seal, drop or failure; an unseal hands out a
// fresh signal). Either drops the in-memory chain. s.mu must be held.
func (s *databaseLogStore) sealed() bool {
	stopped := s.db.Status() == database.StatusSealed
	if !stopped && s.served != nil {
		select {
		case <-s.served:
			stopped = true
		default:
		}
	}
	if stopped {
		s.logs = nil
		s.served = nil
	}
	return stopped
}

// sealedError is the refusal of a call to the sealed database.
func (s *databaseLogStore) sealedError() error {
	return &BarrierSealedError{TenantID: s.tenantID, Database: s.name}
}

// propose applies one encoded OpVerifiableLog command to the database's
// store and returns the apply's result.
func (s *databaseLogStore) propose(ctx context.Context, cmd []byte) (sm.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sealed() {
		return sm.Result{}, s.sealedError()
	}
	backend := s.db.Backend()
	r := backendReader{ctx: ctx, b: backend}
	if s.logs == nil {
		served := s.db.Unserved()
		c := vlog.NewChain()
		if err := c.Restore(r); err != nil {
			return sm.Result{}, s.translateStoreError(err)
		}
		s.logs, s.served = c, served
	}
	s.logs.BeginUpdate()
	b := storage.NewBufferedBatch(r, func(ops []storage.BufferedOp) error {
		return applyToBackend(ctx, backend, ops)
	})
	res, err := statemachine.ApplyVerifiableLog(s.logs, r, b, cmd)
	if err != nil {
		s.logs = nil
		_ = b.Rollback()
		return sm.Result{}, s.translateStoreError(err)
	}
	if err := b.Commit(); err != nil {
		s.logs = nil
		return sm.Result{}, s.translateStoreError(err)
	}
	return res, nil
}

// applyToBackend writes ops to backend in one atomic Batch.
func applyToBackend(ctx context.Context, backend database.Backend, ops []storage.BufferedOp) error {
	if len(ops) == 0 {
		return nil
	}
	batch := make([]database.BatchOp, 0, len(ops))
	for _, op := range ops {
		switch {
		case op.Range:
			return errRangeUnsupported
		case op.Deleted:
			batch = append(batch, database.BatchOp{Op: database.BatchOpDelete, Key: op.Key})
		default:
			batch = append(batch, database.BatchOp{Op: database.BatchOpPut, Key: op.Key, Value: op.Value})
		}
	}
	return backend.Batch(ctx, batch)
}

// read answers one vlog.Request from the database's store.
func (s *databaseLogStore) read(ctx context.Context, req vlog.Request) (interface{}, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sealed() {
		return nil, s.sealedError()
	}
	v, err := vlog.Read(backendReader{ctx: ctx, b: s.db.Backend()}, req)
	if err != nil {
		if errors.As(err, new(*database.SealedError)) {
			return nil, s.translateStoreError(err)
		}
		return nil, &ReadError{Err: err}
	}
	return v, nil
}

// featureEnabled reports true: the database's store is applied by this
// binary alone, so no replica can lack a feature.
func (s *databaseLogStore) featureEnabled(feature.Feature) bool { return true }

// translateStoreError answers a sealed database's error with
// BarrierSealedError, dropping the chain, and returns any other error as it
// is. s.mu must be held.
func (s *databaseLogStore) translateStoreError(err error) error {
	if errors.As(err, new(*database.SealedError)) {
		s.logs = nil
		s.served = nil
		return s.sealedError()
	}
	return err
}

var (
	errReverseUnsupported = errors.New("vlog database store: reverse iteration is not supported")
	errRangeUnsupported   = errors.New("vlog database store: range deletes are not supported")
)

// backendReader is a storage.Reader over a database.Backend.
type backendReader struct {
	ctx context.Context
	b   database.Backend
}

// Get returns key's value, nil when it is absent.
func (r backendReader) Get(key []byte) ([]byte, error) {
	return r.b.Get(r.ctx, key)
}

// NewIterator returns an ascending iterator over the keys in opts' bounds.
func (r backendReader) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	var lower, upper []byte
	if opts != nil {
		if opts.Reverse {
			return storage.NewSliceIterator(nil, nil, errReverseUnsupported)
		}
		lower, upper = opts.LowerBound, opts.UpperBound
	}
	kvs, err := r.b.Range(r.ctx, lower, upper, 0)
	if err != nil {
		return storage.NewSliceIterator(nil, nil, err)
	}
	keys := make([][]byte, len(kvs))
	values := make([][]byte, len(kvs))
	for i, kv := range kvs {
		keys[i], values[i] = kv.Key, kv.Value
	}
	return storage.NewSliceIterator(keys, values, nil)
}
