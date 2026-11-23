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

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/feature"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

// barrierDatabaseTarget serves the verifiable logs of one barrier-enabled
// database from its parent Raft group (owner ruling 1234, option A): the
// logs are proposed to and read from the tenant's group like any other
// database's, and the database's barrier seals what the group holds. An
// append carries each entry's RFC 6962 leaf hash, computed over the
// plaintext, and the entry's body sealed under the database's key (sub-type
// AppendSealed), so the group's tree and every proof are over the plaintext
// while its store holds only ciphertext; a create or key rotation carries
// the software signing key's PKCS8 sealed the same way. Bodies and the key
// are opened on read. Every call is refused with BarrierSealedError while
// the barrier is sealed, and a write holds the barrier's quiesce lease until
// its proposal returns, so a seal never lands between a body sealed and its
// proposal applied.
type barrierDatabaseTarget struct {
	tenantID string
	name     string
	db       *database.Database
	parent   logTarget
}

// databaseSealer is the seal surface of a barrier database's backend
// (database.BarrierBackend).
type databaseSealer interface {
	SealValue(plaintext []byte) ([]byte, error)
	OpenValue(ciphertext []byte) ([]byte, error)
	Hold() (func(), error)
	IsSealed() bool
}

// sealer returns the database's barrier when it is unsealed, else
// BarrierSealedError. The barrier is sealed before the database's status
// follows it, so both are checked.
func (t *barrierDatabaseTarget) sealer() (databaseSealer, error) {
	s, ok := t.db.Backend().(databaseSealer)
	if !ok || s.IsSealed() || t.db.Status() == database.StatusSealed {
		return nil, t.sealedError()
	}
	return s, nil
}

// sealedError is the refusal of a call to the sealed database.
func (t *barrierDatabaseTarget) sealedError() error {
	return &BarrierSealedError{TenantID: t.tenantID, Database: t.name}
}

// translate answers the barrier's seal with BarrierSealedError and returns
// any other error as it is.
func (t *barrierDatabaseTarget) translate(err error) error {
	if errors.As(err, new(*database.SealedError)) {
		return t.sealedError()
	}
	return err
}

// serving refuses a sealed database with BarrierSealedError, else answers
// as the parent group does.
func (t *barrierDatabaseTarget) serving() error {
	if _, err := t.sealer(); err != nil {
		return err
	}
	return t.parent.serving()
}

// featureEnabled reports whether every replica of the parent group carries f.
func (t *barrierDatabaseTarget) featureEnabled(f feature.Feature) bool {
	return t.parent.featureEnabled(f)
}

// propose seals cmd under the database's barrier and proposes it to the
// parent group, holding the barrier's lease until the proposal returns.
func (t *barrierDatabaseTarget) propose(ctx context.Context, cmd []byte) (sm.Result, error) {
	return t.proposeFenced(ctx, cmd, logFence{})
}

// proposeFenced seals cmd and proposes it to the parent group under fence,
// holding the barrier's lease until the proposal returns.
func (t *barrierDatabaseTarget) proposeFenced(ctx context.Context, cmd []byte, fence logFence) (sm.Result, error) {
	s, err := t.sealer()
	if err != nil {
		return sm.Result{}, err
	}
	release, err := s.Hold()
	if err != nil {
		return sm.Result{}, t.translate(err)
	}
	defer release()
	sealed, err := t.seal(s, cmd)
	if err != nil {
		return sm.Result{}, t.translate(err)
	}
	if fenced, ok := t.parent.(fencedLogTarget); ok {
		return fenced.proposeFenced(ctx, sealed, fence)
	}
	if err := fence.live(); err != nil {
		return sm.Result{}, err
	}
	return t.parent.propose(ctx, sealed)
}

// seal rewrites one encoded OpVerifiableLog command so that no plaintext
// entry or signing key leaves the database: an append becomes AppendSealed,
// and a create or key rotation carrying a software key's record carries its
// PKCS8 sealed. Every other sub-type carries no secret and is returned as
// it is.
func (t *barrierDatabaseTarget) seal(s databaseSealer, cmd []byte) ([]byte, error) {
	if len(cmd) == 0 || cmd[0] != protocol.OpVerifiableLog {
		return nil, errNotALogCommand
	}
	subtype, tenantID, body, err := protocol.DecodeVerifiableLogCommand(cmd[1:])
	if err != nil {
		return nil, err
	}
	switch subtype {
	case protocol.VerifiableLogSubtypeAppend:
		if !t.parent.featureEnabled(featuregate.VerifiableLogSealed) {
			return nil, t.featureNotActive(featuregate.VerifiableLogSealed)
		}
		id, entries, err := protocol.DecodeVerifiableLogAppend(tenantID, body)
		if err != nil {
			return nil, err
		}
		leaves := make([]vlog.SealedLeaf, len(entries))
		for i, e := range entries {
			sealed, err := s.SealValue(e)
			if err != nil {
				return nil, err
			}
			leaves[i] = vlog.SealedLeaf{Hash: vlog.LeafHash(e), Body: sealed}
		}
		return protocol.EncodeVerifiableLogAppendSealed(id, leaves)
	case protocol.VerifiableLogSubtypeCreate:
		id, desc, key, err := protocol.DecodeVerifiableLogCreate(tenantID, body)
		if err != nil || key == nil {
			return cmd, err
		}
		sealed, err := sealKey(s, key)
		if err != nil {
			return nil, err
		}
		return protocol.EncodeVerifiableLogCreateWithKey(id, desc, *sealed)
	case protocol.VerifiableLogSubtypeRotateKey:
		id, next, key, err := protocol.DecodeVerifiableLogRotateKey(tenantID, body)
		if err != nil || key == nil {
			return cmd, err
		}
		sealed, err := sealKey(s, key)
		if err != nil {
			return nil, err
		}
		return protocol.EncodeVerifiableLogRotateKey(id, next, sealed)
	default:
		return cmd, nil
	}
}

// featureNotActive is the refusal of a write the parent group's replicas
// cannot all apply yet.
func (t *barrierDatabaseTarget) featureNotActive(f feature.Feature) error {
	e := &LogFeatureNotActiveError{TenantID: t.tenantID, Feature: f}
	if rt, ok := t.parent.(raftTarget); ok {
		e.ShardID = rt.shardID
	}
	return e
}

// sealKey returns a copy of key with its PKCS8 sealed under s.
func sealKey(s databaseSealer, key *vlog.SigningKey) (*vlog.SigningKey, error) {
	sealed, err := s.SealValue(key.PKCS8)
	if err != nil {
		return nil, err
	}
	out := *key
	out.PKCS8 = sealed
	return &out, nil
}

// read answers req from the parent group, opening every entry body and
// signing key record under the database's barrier. A body or key that does
// not open is an error, never returned sealed.
func (t *barrierDatabaseTarget) read(ctx context.Context, req vlog.Request) (interface{}, error) {
	s, err := t.sealer()
	if err != nil {
		return nil, err
	}
	v, err := t.parent.read(ctx, req)
	if err != nil {
		return nil, err
	}
	switch got := v.(type) {
	case []vlog.Entry:
		out := make([]vlog.Entry, len(got))
		for i, e := range got {
			out[i] = e
			if e.Expired || e.Body == nil {
				continue
			}
			if out[i].Body, err = s.OpenValue(e.Body); err != nil {
				return nil, t.translate(err)
			}
		}
		return out, nil
	case *vlog.SigningKey:
		if got == nil {
			return got, nil
		}
		key := *got
		if key.PKCS8, err = s.OpenValue(got.PKCS8); err != nil {
			return nil, t.translate(err)
		}
		return &key, nil
	default:
		return v, nil
	}
}

// logChain is the apply core every verifiable log store outside Raft
// shares (a barrier tenant's standalone store):
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

var (
	errNotALogCommand     = errors.New("vlog database store: the command is not a verifiable log command")
	errReverseUnsupported = errors.New("vlog database store: reverse iteration is not supported")
	errRangeUnsupported   = errors.New("vlog database store: range deletes are not supported")
)
