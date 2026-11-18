package credentials

import (
	"context"
	"errors"
	"log/slog"
)

// layoutPrefixes are the key prefixes the replicated layout keeps a
// credential's records under: the current value, the metadata document
// (which carries the rotation policy and the rotation history), every
// version, a staged rotation, and the migration claims.
var layoutPrefixes = []string{credentialPrefix, MetaKeyPrefix, VersionKeyPrefix, StagedKeyPrefix, MigrationClaimPrefix}

// BindStoreFrom binds st as the credential store, as BindStore does, and
// moves every record of the replicated layout that from holds into st:
// from is the store an earlier binding kept them in, such as the system
// seal barrier a node falls back to before its data plane is known to
// encrypt at rest. The move runs before the node-local migration and
// before any credential operation reads st; one that does not finish now
// (a sealed barrier) is retried before the next operation, whether or not
// the node-local migration already ran. A record st already holds is
// kept: st is the record every node reads. Each moved record is deleted
// from from, so a later binding does not bring back one deleted since.
func (s *CredentialService) BindStoreFrom(ctx context.Context, st, from Store) error {
	if isTypedNilStore(st) || isTypedNilStore(from) {
		return ErrNilBarrier
	}
	if err := s.store.bind(st); err != nil {
		return err
	}
	s.layoutMu.Lock()
	s.layoutFrom = from
	s.layoutMu.Unlock()
	if err := s.ensureLayoutMoved(ctx); err != nil {
		s.logger.Error("credential move from the earlier credential store did not finish",
			slog.String("error", err.Error()))
	}
	if err := s.MigrateLegacy(ctx); err != nil {
		s.logger.Error("credential migration from the node-local layout did not finish",
			slog.String("error", err.Error()))
	}
	return nil
}

// ensureLayoutMoved finishes a move BindStoreFrom started, once.
func (s *CredentialService) ensureLayoutMoved(ctx context.Context) error {
	s.layoutMu.Lock()
	defer s.layoutMu.Unlock()
	if s.layoutFrom == nil {
		return nil
	}
	to, err := s.store.current()
	if err != nil {
		return err
	}
	rctx := ctx
	if s.rotationAttemptTimeout > 0 {
		var cancel context.CancelFunc
		rctx, cancel = context.WithTimeout(ctx, s.rotationAttemptTimeout)
		defer cancel()
	}
	if err := moveLayout(rctx, s.layoutFrom, to); err != nil {
		if cerr := rctx.Err(); cerr != nil && !errors.Is(err, cerr) {
			err = errors.Join(cerr, err)
		}
		return &OperationError{Op: "move credential store", Cause: err}
	}
	s.layoutFrom = nil
	return nil
}

// moveLayout copies every replicated-layout record from holds into to,
// keeping a record to already holds, and only once every copy is written
// deletes them from from: a move cut short copies again next time.
func moveLayout(ctx context.Context, from, to Store) error {
	var moved []string
	for _, prefix := range layoutPrefixes {
		keys, err := from.List(ctx, prefix)
		if err != nil {
			return err
		}
		for _, key := range keys {
			value, err := from.Get(ctx, key)
			if err != nil {
				return err
			}
			if _, err := to.PutIfAbsent(ctx, key, value); err != nil {
				return err
			}
			moved = append(moved, key)
		}
	}
	for _, key := range moved {
		if err := from.Delete(ctx, key); err != nil {
			return err
		}
	}
	return nil
}
