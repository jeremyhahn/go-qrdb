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

package lock

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/google/uuid"

	"github.com/jeremyhahn/go-qrdb/pkg/clock"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// Locker is the distributed lock interface. All methods are safe for
// concurrent use.
type Locker interface {
	// Lock acquires the distributed lock for opts.Key. It blocks until
	// the lock is acquired or the context is canceled.
	Lock(ctx context.Context, opts LockOptions) (*Lock, error)

	// TryLock attempts to acquire the lock without blocking. It returns
	// LockHeldError immediately when the lock is held by another owner.
	TryLock(ctx context.Context, opts LockOptions) (*Lock, error)

	// Unlock releases the lock identified by key and fencingToken.
	// Returns NotLockHolderError when the token does not match.
	Unlock(ctx context.Context, key string, fencingToken int64) error

	// Extend refreshes the TTL of an existing lock via keepalive.
	// Returns NotLockHolderError when the token does not match.
	Extend(ctx context.Context, key string, fencingToken int64) (*Lock, error)

	// Status returns the current lock state for key, or nil when the
	// key is not locked.
	Status(ctx context.Context, key string) (*Lock, error)

	// List returns all locks whose keys start with the given prefix.
	// An empty prefix returns all active locks.
	List(ctx context.Context, prefix string) ([]*Lock, error)
}

// Service implements Locker by composing existing QRDB service interfaces.
// No new engine opcodes are required; all operations use the transaction,
// lease, watch, and MVCC KV services.
type Service struct {
	txSvc    TransactionServicer
	leaseSvc LeaseServicer
	watchSvc WatchServicer
	kvSvc    MVCCKVServicer
	logger   *slog.Logger
	clock    clock.Clock
}

// Compile-time interface check.
var _ Locker = (*Service)(nil)

// Option configures a Service.
type Option func(*Service)

// WithClock sets the time source that stamps a lock's acquisition time. The
// default is clock.Real. A lock's expiry is not decided here but by the lease
// beneath it, whose clock is configured on the lessor (lease.WithClock); a test
// that drives both from one clocktest.Fake sees acquisition time and expiry
// agree. A nil Clock leaves the default in place.
func WithClock(c clock.Clock) Option {
	return func(s *Service) {
		if c != nil {
			s.clock = c
		}
	}
}

// NewLocker creates a new lock Service. All four service dependencies are
// required. A no-op logger is used when logger is nil.
func NewLocker(
	txSvc TransactionServicer,
	leaseSvc LeaseServicer,
	watchSvc WatchServicer,
	kvSvc MVCCKVServicer,
	logger *slog.Logger,
	opts ...Option,
) (*Service, error) {
	if txSvc == nil {
		return nil, ErrNilTxService
	}
	if leaseSvc == nil {
		return nil, ErrNilLeaseService
	}
	if watchSvc == nil {
		return nil, ErrNilWatchService
	}
	if kvSvc == nil {
		return nil, ErrNilKVService
	}
	if logger == nil {
		logger = slog.Default()
	}
	s := &Service{
		txSvc:    txSvc,
		leaseSvc: leaseSvc,
		watchSvc: watchSvc,
		kvSvc:    kvSvc,
		logger:   logger,
		clock:    clock.Real(),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// TryLock attempts to acquire the lock without blocking. It returns
// immediately with LockHeldError when the lock is held by a different
// owner, or with the acquired Lock on success.
func (s *Service) TryLock(ctx context.Context, opts LockOptions) (*Lock, error) {
	if opts.Key == "" {
		return nil, ErrEmptyKey
	}
	if opts.TTL <= 0 {
		return nil, ErrInvalidTTL
	}
	if opts.HolderID == "" {
		opts.HolderID = uuid.NewString()
	}

	ttlSecs := int64(opts.TTL.Seconds())
	if ttlSecs < 1 {
		ttlSecs = 1
	}

	lease, err := s.leaseSvc.Grant(ctx, ttlSecs)
	if err != nil {
		return nil, err
	}

	now := s.clock.Now().Unix()
	lv := lockValue{
		HolderID:     opts.HolderID,
		Reentrant:    opts.Reentrant,
		AcquireCount: 1,
		AcquiredAt:   now,
		LeaseID:      lease.ID,
		TTL:          ttlSecs,
	}

	jsonValue, err := json.Marshal(lv)
	if err != nil {
		_ = s.leaseSvc.Revoke(ctx, lease.ID)
		return nil, err
	}

	// CAS: key version == 0 (key does not exist).
	compares := []Compare{
		{
			Key:     []byte(lockKey(opts.Key)),
			Target:  CompareVersion,
			Result:  CompareEqual,
			Version: 0,
		},
	}
	successOps := []Op{
		{
			Type:    OpPut,
			Key:     []byte(lockKey(opts.Key)),
			Value:   jsonValue,
			LeaseID: lease.ID,
		},
	}

	resp, err := s.txSvc.Execute(ctx, compares, successOps, nil)
	if err != nil {
		// The transaction's outcome may be unknown (go-quicraft committed
		// the proposal and then failed to deliver the response): revoking
		// the lease here would destroy a lock this attempt may have
		// actually acquired. Re-read the key first -- if it is now bound to
		// exactly the lease this attempt just granted, the PUT committed
		// despite the error, and the lock is genuinely held, so return it
		// instead of revoking and reporting failure. Anything else (a read
		// failure, a different lease, no key at all) leaves the original
		// error as the honest answer, matching the pattern
		// PromoteJoinIdentityAdmitted uses for the identical ambiguity.
		if current, getErr := s.kvSvc.GetRevision(ctx, lockKey(opts.Key)); getErr == nil && current.LeaseID == lease.ID {
			return &Lock{
				Key:          opts.Key,
				FencingToken: current.ModRevision,
				LeaseID:      lease.ID,
				HolderID:     opts.HolderID,
				Reentrant:    opts.Reentrant,
				AcquireCount: 1,
				TTL:          ttlSecs,
				AcquiredAt:   now,
			}, nil
		}
		_ = s.leaseSvc.Revoke(ctx, lease.ID)
		return nil, err
	}

	if resp.Succeeded {
		// Store the fencing token in the value so it survives reentrant
		// updates. The token is the MVCC revision of the initial acquisition.
		lv.FencingToken = resp.Revision
		if updated, err := json.Marshal(lv); err == nil {
			// Best-effort update to persist the fencing token. If this fails,
			// the token is still returned to the caller from resp.Revision.
			updateOps := []Op{{Type: OpPut, Key: []byte(lockKey(opts.Key)), Value: updated, LeaseID: lease.ID}}
			_, _ = s.txSvc.Execute(ctx, nil, updateOps, nil)
		}

		return &Lock{
			Key:          opts.Key,
			FencingToken: resp.Revision,
			LeaseID:      lease.ID,
			HolderID:     opts.HolderID,
			Reentrant:    opts.Reentrant,
			AcquireCount: 1,
			TTL:          ttlSecs,
			AcquiredAt:   now,
		}, nil
	}

	// CAS failed: the key already exists. Read the current holder.
	current, err := s.kvSvc.GetRevision(ctx, lockKey(opts.Key))
	if err != nil {
		_ = s.leaseSvc.Revoke(ctx, lease.ID)
		return nil, err
	}

	var existing lockValue
	if err := json.Unmarshal(current.Value, &existing); err != nil {
		_ = s.leaseSvc.Revoke(ctx, lease.ID)
		return nil, err
	}

	// Reentrant re-acquisition by the same holder. Both the caller and
	// the existing lock must have reentrant enabled to allow re-entry.
	if existing.HolderID == opts.HolderID && opts.Reentrant && existing.Reentrant {
		existing.AcquireCount++

		updated, err := json.Marshal(existing)
		if err != nil {
			_ = s.leaseSvc.Revoke(ctx, lease.ID)
			return nil, err
		}

		// Revoke the newly granted lease -- we reuse the existing one.
		_ = s.leaseSvc.Revoke(ctx, lease.ID)

		putOps := []Op{
			{
				Type:    OpPut,
				Key:     []byte(lockKey(opts.Key)),
				Value:   updated,
				LeaseID: existing.LeaseID,
			},
		}
		if _, err := s.txSvc.Execute(ctx, nil, putOps, nil); err != nil {
			return nil, err
		}

		// Return the stored fencing token from the initial acquisition.
		// Reentrant re-acquisition does not change ownership, so the
		// fencing token remains stable across re-entries.
		return &Lock{
			Key:          opts.Key,
			FencingToken: existing.FencingToken,
			LeaseID:      existing.LeaseID,
			HolderID:     existing.HolderID,
			Reentrant:    existing.Reentrant,
			AcquireCount: existing.AcquireCount,
			TTL:          existing.TTL,
			AcquiredAt:   existing.AcquiredAt,
		}, nil
	}

	// Non-reentrant or different holder: revoke newly granted lease and report held.
	_ = s.leaseSvc.Revoke(ctx, lease.ID)
	return nil, &LockHeldError{Key: opts.Key, HolderID: existing.HolderID}
}

// Lock acquires the distributed lock, blocking until the lock is available
// or the context is canceled. When TryLock succeeds on the first attempt,
// it returns immediately. On LockHeldError, Lock watches the key for delete
// events and retries TryLock on each release.
func (s *Service) Lock(ctx context.Context, opts LockOptions) (*Lock, error) {
	acquired, err := s.TryLock(ctx, opts)
	if err == nil {
		return acquired, nil
	}

	var held *LockHeldError
	if !errors.As(err, &held) {
		return nil, err
	}

	watcher, err := s.watchSvc.Watch(ctx, lockKey(opts.Key))
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = watcher.Close()
	}()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case resp, ok := <-watcher.Events():
			if !ok {
				// Watcher closed; retry once before giving up.
				acquired, err = s.TryLock(ctx, opts)
				if err == nil {
					return acquired, nil
				}
				return nil, err
			}
			for _, evt := range resp.Events {
				if evt == nil {
					continue
				}
				if evt.Type == watch.EventDelete {
					acquired, err = s.TryLock(ctx, opts)
					if err == nil {
						return acquired, nil
					}
					var heldAgain *LockHeldError
					if errors.As(err, &heldAgain) {
						// Another holder raced us; continue waiting.
						continue
					}
					return nil, err
				}
			}
		}
	}
}

// Unlock releases the lock identified by key and fencingToken. For
// reentrant locks with an AcquireCount greater than one, the count is
// decremented rather than the lock being released. Returns
// NotLockHolderError when the token does not match the current lock.
func (s *Service) Unlock(ctx context.Context, key string, fencingToken int64) error {
	current, err := s.kvSvc.GetRevision(ctx, lockKey(key))
	if err != nil {
		var knf interface{ Error() string }
		if errors.As(err, &knf) {
			// Translate KeyNotFoundError to LockNotFoundError.
			return &LockNotFoundError{Key: key}
		}
		return err
	}
	if current == nil {
		return &LockNotFoundError{Key: key}
	}

	var lv lockValue
	if err := json.Unmarshal(current.Value, &lv); err != nil {
		return err
	}

	// Validate the fencing token against the stored token (set on
	// initial acquisition and preserved through reentrant updates).
	storedToken := lv.FencingToken
	if storedToken == 0 {
		// Backwards compat: locks created before FencingToken was persisted
		// fall back to ModRevision.
		storedToken = current.ModRevision
	}
	if storedToken != fencingToken {
		return &NotLockHolderError{
			Key:           key,
			ExpectedToken: fencingToken,
			ActualToken:   storedToken,
		}
	}

	// Reentrant lock with remaining acquisitions: decrement and update.
	if lv.Reentrant && lv.AcquireCount > 1 {
		lv.AcquireCount--
		updated, err := json.Marshal(lv)
		if err != nil {
			return err
		}
		putOps := []Op{
			{
				Type:    OpPut,
				Key:     []byte(lockKey(key)),
				Value:   updated,
				LeaseID: lv.LeaseID,
			},
		}
		_, err = s.txSvc.Execute(ctx, nil, putOps, nil)
		return err
	}

	// CAS delete: only delete if the current revision still matches
	// (no concurrent modification since our read).
	compares := []Compare{
		{
			Key:         []byte(lockKey(key)),
			Target:      CompareModRevision,
			Result:      CompareEqual,
			ModRevision: current.ModRevision,
		},
	}
	deleteOps := []Op{
		{
			Type: OpDelete,
			Key:  []byte(lockKey(key)),
		},
	}

	resp, err := s.txSvc.Execute(ctx, compares, deleteOps, nil)
	if err != nil {
		return err
	}

	if !resp.Succeeded {
		// Another holder modified the key between our read and delete.
		// Use resp.Revision as the actual token since current.ModRevision
		// matched fencingToken at the time of our initial read.
		return &NotLockHolderError{
			Key:           key,
			ExpectedToken: fencingToken,
			ActualToken:   resp.Revision,
		}
	}

	// Revoke the lease to immediately clean up the key.
	return s.leaseSvc.Revoke(ctx, lv.LeaseID)
}

// Extend refreshes the TTL of an existing lock by issuing a lease keepalive.
// Returns NotLockHolderError when the supplied fencing token does not match.
func (s *Service) Extend(ctx context.Context, key string, fencingToken int64) (*Lock, error) {
	current, err := s.kvSvc.GetRevision(ctx, lockKey(key))
	if err != nil {
		return nil, &LockNotFoundError{Key: key}
	}
	if current == nil {
		return nil, &LockNotFoundError{Key: key}
	}

	var lv lockValue
	if err := json.Unmarshal(current.Value, &lv); err != nil {
		return nil, err
	}

	storedToken := lv.FencingToken
	if storedToken == 0 {
		storedToken = current.ModRevision
	}
	if storedToken != fencingToken {
		return nil, &NotLockHolderError{
			Key:           key,
			ExpectedToken: fencingToken,
			ActualToken:   storedToken,
		}
	}

	refreshed, err := s.leaseSvc.KeepAlive(ctx, lv.LeaseID)
	if err != nil {
		return nil, err
	}

	return &Lock{
		Key:          key,
		FencingToken: lv.FencingToken,
		LeaseID:      lv.LeaseID,
		HolderID:     lv.HolderID,
		Reentrant:    lv.Reentrant,
		AcquireCount: lv.AcquireCount,
		TTL:          refreshed.TTL,
		AcquiredAt:   lv.AcquiredAt,
	}, nil
}

// Status returns the current lock state for the given key. Returns nil, nil
// when the key is not currently locked.
func (s *Service) Status(ctx context.Context, key string) (*Lock, error) {
	current, err := s.kvSvc.GetRevision(ctx, lockKey(key))
	if err != nil {
		// A KeyNotFoundError means the key is not locked; return nil, nil.
		return nil, nil
	}
	if current == nil {
		return nil, nil
	}

	var lv lockValue
	if err := json.Unmarshal(current.Value, &lv); err != nil {
		return nil, err
	}

	return &Lock{
		Key:          key,
		FencingToken: lv.FencingToken,
		LeaseID:      lv.LeaseID,
		HolderID:     lv.HolderID,
		Reentrant:    lv.Reentrant,
		AcquireCount: lv.AcquireCount,
		TTL:          lv.TTL,
		AcquiredAt:   lv.AcquiredAt,
	}, nil
}

// List returns all locks whose user-facing keys start with prefix. An
// empty prefix returns all active locks. The range end is computed by
// incrementing the last byte of the full internal prefix+prefix key.
func (s *Service) List(ctx context.Context, prefix string) ([]*Lock, error) {
	start := []byte(lockKeyPrefix + prefix)
	end := prefixEnd(start)

	kvs, _, _, err := s.kvSvc.Range(ctx, start, end)
	if err != nil {
		return nil, err
	}

	locks := make([]*Lock, 0, len(kvs))
	for _, kv := range kvs {
		var lv lockValue
		if err := json.Unmarshal(kv.Value, &lv); err != nil {
			s.logger.Warn("lock: skipping malformed lock entry",
				"key", string(kv.Key),
				"error", err)
			continue
		}
		locks = append(locks, &Lock{
			Key:          userKey(string(kv.Key)),
			FencingToken: lv.FencingToken,
			LeaseID:      lv.LeaseID,
			HolderID:     lv.HolderID,
			Reentrant:    lv.Reentrant,
			AcquireCount: lv.AcquireCount,
			TTL:          lv.TTL,
			AcquiredAt:   lv.AcquiredAt,
		})
	}
	return locks, nil
}

// prefixEnd computes the key immediately after all keys that share the
// given prefix by incrementing the last non-0xFF byte. Returns nil when
// all bytes are 0xFF (scan to end).
func prefixEnd(prefix []byte) []byte {
	end := make([]byte, len(prefix))
	copy(end, prefix)
	for i := len(end) - 1; i >= 0; i-- {
		if end[i] < 0xFF {
			end[i]++
			return end[:i+1]
		}
	}
	return nil
}
