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
	"time"

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
	// Returns NotLockHolderError when the token does not match, and
	// ErrUnlockContended when concurrent writes to the lock outlast its
	// retries.
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
	// tokenFieldRetired reports whether every node reads the fencing token
	// from the key's create revision; nil means it does.
	tokenFieldRetired func() bool
	// isRetryable reports whether a failed write may land if sent again;
	// nil means none may.
	isRetryable func(error) bool
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

// WithTokenFieldRetired supplies the check that every node serving the lock
// keys reads the fencing token from the key's create revision. Until it
// reports true the lock record keeps the fencing_token field a binary from
// before that reads: an acquisition stores it, and a re-entry or a reentrant
// release's decrement rewrites it, always with the create revision, so both
// formats name the same token. Once it reports true the field is dropped.
// Without this option the field is never written, which is right only where
// every node runs this binary.
func WithTokenFieldRetired(retired func() bool) Option {
	return func(s *Service) {
		s.tokenFieldRetired = retired
	}
}

// WithRetryable supplies the check that a write which failed with an error
// may land if it is sent again: the transport could not say whether it was
// applied, or the shard's leader changed under it. recordToken retries only
// such a failure. Without this option no failure is retried.
func WithRetryable(retryable func(error) bool) Option {
	return func(s *Service) {
		s.isRetryable = retryable
	}
}

const (
	// maxTokenStoreAttempts bounds recordToken's writes of one fencing token,
	// the first included.
	maxTokenStoreAttempts = 4

	// tokenStoreBackoff is recordToken's wait between two attempts.
	tokenStoreBackoff time.Duration = 50 * time.Millisecond
)

// storedToken is the fencing_token a write of the lock record carries: the
// create revision while a node may still read the field, and zero, which
// omits it, once none does.
func (s *Service) storedToken(createRevision int64) int64 {
	if s.tokenFieldRetired == nil || s.tokenFieldRetired() {
		return 0
	}
	return createRevision
}

// recordToken stores the fencing token in the record the acquire just
// created, for a node that reads the field. It is a compare-and-swap on the
// acquire's own revision, so it writes nothing once the lock has been
// released, re-entered (which stores the token itself) or re-acquired, and
// can never write a released key back. A write that fails with an error
// WithRetryable classes as retryable is sent again, the same compare-and-swap,
// after tokenStoreBackoff, up to maxTokenStoreAttempts in all and never past
// ctx; a compare that lost is not, since the record is no longer the one this
// acquire wrote. A write still failing then leaves the lock held and is
// logged: the field is advisory to this binary.
func (s *Service) recordToken(ctx context.Context, key string, createRevision int64, lv lockValue) {
	lv.FencingToken = s.storedToken(createRevision)
	if lv.FencingToken == 0 {
		return
	}
	updated, err := json.Marshal(lv)
	if err != nil {
		s.logger.Warn("lock: storing the fencing token for older nodes failed",
			"key", key, "fencing_token", createRevision, "error", err)
		return
	}
	compares := []Compare{{
		Key:         []byte(lockKey(key)),
		Target:      CompareModRevision,
		Result:      CompareEqual,
		ModRevision: createRevision,
	}}
	ops := []Op{{
		Type:    OpPut,
		Key:     []byte(lockKey(key)),
		Value:   updated,
		LeaseID: lv.LeaseID,
	}}
	for attempt := 1; ; attempt++ {
		resp, err := s.txSvc.Execute(ctx, compares, ops, nil)
		if err == nil {
			if !resp.Succeeded {
				s.logger.Info("lock: fencing token not stored, the record moved",
					"key", key, "fencing_token", createRevision, "attempts", attempt)
			}
			return
		}
		if attempt == maxTokenStoreAttempts || !s.retryable(err) || !s.backoff(ctx) {
			s.logger.Warn("lock: storing the fencing token for older nodes failed",
				"key", key, "fencing_token", createRevision, "attempts", attempt, "error", err)
			return
		}
	}
}

// retryable reports whether a write that failed with err may land if sent
// again; without WithRetryable nothing is.
func (s *Service) retryable(err error) bool {
	return s.isRetryable != nil && s.isRetryable(err)
}

// backoff waits tokenStoreBackoff before recordToken's next attempt. It
// reports false, without waiting it out, when ctx ends first.
func (s *Service) backoff(ctx context.Context) bool {
	timer := s.clock.NewTimer(tokenStoreBackoff)
	defer timer.Stop()
	select {
	case <-timer.C():
		return true
	case <-ctx.Done():
		return false
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
// owner, or with the acquired Lock on success. A lock released while TryLock
// reads its holder, or re-enters it, is free, so TryLock takes it rather than
// report it held or write it back onto the released lease.
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

	// A release that lands between a failed CAS and the read of the holder
	// leaves no key to read, and one that lands between that read and a
	// reentrant update fails the update's CAS: either way the lock may be
	// free, so the CAS is retried with the lease already granted. Every retry
	// follows another caller's completed write to the key, so the loop makes
	// progress; it ends with ctx.
	for {
		resp, err := s.txSvc.Execute(ctx, compares, successOps, nil)
		if err != nil {
			// The transaction's outcome may be unknown (go-quicraft committed
			// the proposal and then failed to deliver the response): revoking
			// the lease here would destroy a lock this attempt may have
			// actually acquired. Re-read the key first -- if it is now bound
			// to exactly the lease this attempt just granted, the PUT
			// committed despite the error, and the lock is genuinely held, so
			// return it instead of revoking and reporting failure. Anything
			// else (a read failure, a different lease, no key at all) leaves
			// the original error as the honest answer, matching the pattern
			// PromoteJoinIdentityAdmitted uses for the identical ambiguity.
			if committed, getErr := s.kvSvc.GetRevision(ctx, lockKey(opts.Key)); getErr == nil && committed.LeaseID == lease.ID {
				s.recordToken(ctx, opts.Key, committed.CreateRevision, lv)
				return &Lock{
					Key:          opts.Key,
					FencingToken: committed.CreateRevision,
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
			// The transaction's one put created the key, so its revision
			// is the key's create revision: the fencing token.
			s.recordToken(ctx, opts.Key, resp.Revision, lv)
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
		if isKeyNotFound(err) {
			current = nil
		} else if err != nil {
			_ = s.leaseSvc.Revoke(ctx, lease.ID)
			return nil, err
		}
		if current != nil {
			var existing lockValue
			if err := json.Unmarshal(current.Value, &existing); err != nil {
				_ = s.leaseSvc.Revoke(ctx, lease.ID)
				return nil, err
			}

			// Reentrant re-acquisition by the same holder. Both the caller
			// and the existing lock must have reentrant enabled to allow
			// re-entry.
			if existing.HolderID != opts.HolderID || !opts.Reentrant || !existing.Reentrant {
				_ = s.leaseSvc.Revoke(ctx, lease.ID)
				return nil, &LockHeldError{Key: opts.Key, HolderID: existing.HolderID}
			}
			lock, reentered, err := s.reenter(ctx, opts.Key, current.CreateRevision, current.ModRevision, existing)
			if err != nil || reentered {
				// The update reuses the existing lease, so the newly
				// granted one is not needed whatever its outcome.
				_ = s.leaseSvc.Revoke(ctx, lease.ID)
				return lock, err
			}
		}
		if err := ctx.Err(); err != nil {
			_ = s.leaseSvc.Revoke(ctx, lease.ID)
			return nil, err
		}
	}
}

// reenter increments the acquire count of the reentrant lock existing, read
// at modRevision, keeping it bound to its own lease. The update is a CAS on
// modRevision: a release (or another re-entry) since the read fails it, and
// reenter reports false rather than write the lock back onto a lease the
// release has revoked. The returned lock carries createRevision, the key's
// create revision, as its fencing token: a re-entry does not change ownership.
func (s *Service) reenter(ctx context.Context, key string, createRevision, modRevision int64, existing lockValue) (*Lock, bool, error) {
	existing.AcquireCount++
	existing.FencingToken = s.storedToken(createRevision)

	updated, err := json.Marshal(existing)
	if err != nil {
		return nil, false, err
	}

	compares := []Compare{
		{
			Key:         []byte(lockKey(key)),
			Target:      CompareModRevision,
			Result:      CompareEqual,
			ModRevision: modRevision,
		},
	}
	putOps := []Op{
		{
			Type:    OpPut,
			Key:     []byte(lockKey(key)),
			Value:   updated,
			LeaseID: existing.LeaseID,
		},
	}
	resp, err := s.txSvc.Execute(ctx, compares, putOps, nil)
	if err != nil {
		return nil, false, err
	}
	if !resp.Succeeded {
		return nil, false, nil
	}

	return &Lock{
		Key:          key,
		FencingToken: createRevision,
		LeaseID:      existing.LeaseID,
		HolderID:     existing.HolderID,
		Reentrant:    existing.Reentrant,
		AcquireCount: existing.AcquireCount,
		TTL:          existing.TTL,
		AcquiredAt:   existing.AcquiredAt,
	}, true, nil
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
// NotLockHolderError when the token does not match the current lock, and
// ErrUnlockContended when every one of maxUnlockAttempts rounds loses its
// compare-and-swap to a concurrent write.
func (s *Service) Unlock(ctx context.Context, key string, fencingToken int64) error {
	// A write that lands between the read and the decrement or delete fails
	// that CAS, so the read is retried: it re-checks the token and finds the
	// new count, or no lock at all. Every retry follows another caller's
	// completed write to the key, so the loop makes progress; it is bounded
	// by maxUnlockAttempts and ends with ctx.
	for range maxUnlockAttempts {
		current, err := s.kvSvc.GetRevision(ctx, lockKey(key))
		if err != nil {
			if isKeyNotFound(err) {
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

		// The fencing token is the key's create revision, which re-entries
		// leave unchanged and a re-acquisition always raises.
		if current.CreateRevision != fencingToken {
			return &NotLockHolderError{
				Key:           key,
				ExpectedToken: fencingToken,
				ActualToken:   current.CreateRevision,
			}
		}

		var done bool
		if !lv.Reentrant || lv.AcquireCount <= 1 {
			done, err = s.release(ctx, key, current.ModRevision, lv.LeaseID)
		} else {
			// Reentrant lock with remaining acquisitions: decrement and update.
			done, err = s.decrement(ctx, key, current.CreateRevision, current.ModRevision, lv)
		}
		if err != nil || done {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return ErrUnlockContended
}

// maxUnlockAttempts bounds the read and compare-and-swap rounds of one
// Unlock. Each lost round follows a completed write to the lock key by its
// own holder, so exhausting it takes a sustained stream of such writes.
const maxUnlockAttempts = 16

// decrement writes lv back with one acquisition fewer, as a CAS on the
// ModRevision read: it reports false when the key changed since that read.
func (s *Service) decrement(ctx context.Context, key string, createRevision, modRevision int64, lv lockValue) (bool, error) {
	lv.AcquireCount--
	lv.FencingToken = s.storedToken(createRevision)
	updated, err := json.Marshal(lv)
	if err != nil {
		return false, err
	}
	compares := []Compare{
		{
			Key:         []byte(lockKey(key)),
			Target:      CompareModRevision,
			Result:      CompareEqual,
			ModRevision: modRevision,
		},
	}
	putOps := []Op{
		{
			Type:    OpPut,
			Key:     []byte(lockKey(key)),
			Value:   updated,
			LeaseID: lv.LeaseID,
		},
	}
	resp, err := s.txSvc.Execute(ctx, compares, putOps, nil)
	if err != nil {
		return false, err
	}
	return resp.Succeeded, nil
}

// release deletes the lock key, as a CAS on the ModRevision read, and
// revokes its lease: it reports false, deleting nothing, when the key
// changed since that read.
func (s *Service) release(ctx context.Context, key string, modRevision, leaseID int64) (bool, error) {
	// CAS delete: only delete if the current revision still matches
	// (no concurrent modification since our read).
	compares := []Compare{
		{
			Key:         []byte(lockKey(key)),
			Target:      CompareModRevision,
			Result:      CompareEqual,
			ModRevision: modRevision,
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
		return false, err
	}
	if !resp.Succeeded {
		return false, nil
	}

	// Revoke the lease to immediately clean up the key.
	return true, s.leaseSvc.Revoke(ctx, leaseID)
}

// Extend refreshes the TTL of an existing lock by issuing a lease keepalive.
// Returns NotLockHolderError when the supplied fencing token does not match.
func (s *Service) Extend(ctx context.Context, key string, fencingToken int64) (*Lock, error) {
	current, err := s.kvSvc.GetRevision(ctx, lockKey(key))
	if err != nil {
		if isKeyNotFound(err) {
			return nil, &LockNotFoundError{Key: key}
		}
		return nil, err
	}
	if current == nil {
		return nil, &LockNotFoundError{Key: key}
	}

	var lv lockValue
	if err := json.Unmarshal(current.Value, &lv); err != nil {
		return nil, err
	}

	if current.CreateRevision != fencingToken {
		return nil, &NotLockHolderError{
			Key:           key,
			ExpectedToken: fencingToken,
			ActualToken:   current.CreateRevision,
		}
	}

	refreshed, err := s.leaseSvc.KeepAlive(ctx, lv.LeaseID)
	if err != nil {
		return nil, err
	}

	return &Lock{
		Key:          key,
		FencingToken: current.CreateRevision,
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
		// Only a missing key means the key is not locked; any other
		// error is a failed read the caller must see.
		if isKeyNotFound(err) {
			return nil, nil
		}
		return nil, err
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
		FencingToken: current.CreateRevision,
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
			FencingToken: kv.CreateRevision,
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
