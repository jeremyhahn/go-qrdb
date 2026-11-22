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
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
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
	logs   logChain
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
	if s.served != nil {
		select {
		case <-s.served:
			// The incarnation the chain was restored under stopped serving:
			// the chain is stale even if the database serves again now.
			s.logs.drop()
			s.served = nil
		default:
		}
	}
	if s.db.Status() == database.StatusSealed || barrierSealed(s.db.Backend()) {
		s.logs.drop()
		s.served = nil
		return true
	}
	return false
}

// sealedError is the refusal of a call to the sealed database.
func (s *databaseLogStore) sealedError() error {
	return &BarrierSealedError{TenantID: s.tenantID, Database: s.name}
}

// barrierSealed reports whether backend is a barrier whose seal is in
// place: the barrier is sealed before the database's status follows it.
func barrierSealed(backend database.Backend) bool {
	sealed, ok := backend.(interface{ IsSealed() bool })
	return ok && sealed.IsSealed()
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
	if s.logs.chain == nil {
		served := s.db.Unserved()
		if err := s.logs.restore(r); err != nil {
			return sm.Result{}, s.translateStoreError(err)
		}
		s.served = served
	}
	res, err := s.logs.apply(r, func(ops []storage.BufferedOp) error {
		return applyToBackend(ctx, backend, ops)
	}, cmd)
	if err != nil {
		return sm.Result{}, s.translateStoreError(err)
	}
	return res, nil
}

// logChain is the apply core every verifiable log store outside Raft
// shares (a barrier database's store, a barrier tenant's standalone store):
// one vlog.Chain restored from the store and every command applied by
// statemachine.ApplyVerifiableLog, exactly as the engine applies it, its
// writes staged and handed to the store's atomic commit in one call. chain
// is nil until restored and is set back to nil on any apply or commit
// failure; the owning store drops it (drop) whenever its store stops
// serving, so no head state outlives a seal. The owner serializes every
// call.
type logChain struct {
	chain *vlog.Chain
}

// drop forgets the in-memory chain; the next apply restores it.
func (c *logChain) drop() { c.chain = nil }

// restore rebuilds the chain from r.
func (c *logChain) restore(r storage.Reader) error {
	ch := vlog.NewChain()
	if err := ch.Restore(r); err != nil {
		return err
	}
	c.chain = ch
	return nil
}

// apply applies one encoded OpVerifiableLog command (its OpVerifiableLog
// byte first, as a group proposal carries it) over r, restoring the chain
// first when it is nil, and hands every staged write to commit in one call.
// A failed apply rolls back and a failed apply or commit drops the chain.
func (c *logChain) apply(r storage.Reader, commit func([]storage.BufferedOp) error, cmd []byte) (sm.Result, error) {
	// The state machines apply the payload after the op byte, and so does
	// the store.
	if len(cmd) == 0 || cmd[0] != protocol.OpVerifiableLog {
		return sm.Result{}, errNotALogCommand
	}
	payload := cmd[1:]
	if c.chain == nil {
		if err := c.restore(r); err != nil {
			return sm.Result{}, err
		}
	}
	c.chain.BeginUpdate()
	b := storage.NewBufferedBatch(r, commit)
	res, err := statemachine.ApplyVerifiableLog(c.chain, r, b, payload)
	if err != nil {
		c.chain = nil
		_ = b.Rollback()
		return sm.Result{}, err
	}
	if err := b.Commit(); err != nil {
		c.chain = nil
		return sm.Result{}, err
	}
	return res, nil
}

// readLog answers one vlog.Request from r. An error sealed reports as the
// store's seal is returned as it is, for the owner to translate; any other
// is a ReadError.
func readLog(r storage.Reader, req vlog.Request, sealed func(error) bool) (interface{}, error) {
	v, err := vlog.Read(r, req)
	if err != nil {
		if sealed(err) {
			return nil, err
		}
		return nil, &ReadError{Err: err}
	}
	return v, nil
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
	v, err := readLog(backendReader{ctx: ctx, b: s.db.Backend()}, req, func(err error) bool {
		return errors.As(err, new(*database.SealedError))
	})
	if err != nil {
		return nil, s.translateStoreError(err)
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
		s.logs.drop()
		s.served = nil
		return s.sealedError()
	}
	return err
}

var (
	errNotALogCommand     = errors.New("vlog database store: the command is not a verifiable log command")
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
