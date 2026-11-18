package credentials

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"slices"
	"sort"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
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
		var conflict *LayoutConflictError
		if errors.As(err, &conflict) {
			// Every other name moved and a conflict does not clear on a
			// retry: the move is over. Report it once and let the
			// operation that triggered it go on against the new store.
			s.layoutFrom = nil
			s.logger.Warn("credential records left in the earlier credential store",
				slog.String("error", err.Error()))
			return nil
		}
		if cerr := rctx.Err(); cerr != nil && !errors.Is(err, cerr) {
			err = errors.Join(cerr, err)
		}
		return &OperationError{Op: "move credential store", Cause: err}
	}
	s.layoutFrom = nil
	return nil
}

// layoutSet is every replicated-layout record of one credential name: its
// current value, metadata, version history, staged rotations and
// migration claim. A set moves whole or not at all.
type layoutSet struct {
	name string
	// enc is the name segment the version and staged keys carry.
	enc  string
	keys []string
}

// layoutSetOf names the credential a replicated-layout key belongs to and
// the name segment its version and staged keys carry.
func layoutSetOf(prefix, key string) (name, enc string) {
	rest := strings.TrimPrefix(key, prefix)
	if prefix != VersionKeyPrefix && prefix != StagedKeyPrefix {
		return rest, encodedName(rest)
	}
	seg, _, _ := strings.Cut(rest, "/")
	raw, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		// A segment the service never wrote: group it by itself.
		return seg, seg
	}
	return string(raw), seg
}

// moveLayout moves every replicated-layout record from from into to, one
// credential name at a time. A name to already holds any record of is not
// moved: both copies stay where they are and the name is reported in a
// *LayoutConflictError once every other name has moved. Every other name's
// records are written in one guarded batch that requires each destination
// key absent, and only a batch that applied deletes its source records, so
// a move cut short copies again next time. Each value read from from is
// wiped once its write attempt is over.
func moveLayout(ctx context.Context, from, to Store) error {
	sets := map[string]*layoutSet{}
	var order []string
	for _, prefix := range layoutPrefixes {
		keys, err := from.List(ctx, prefix)
		if err != nil {
			return err
		}
		for _, key := range keys {
			name, enc := layoutSetOf(prefix, key)
			set, ok := sets[name]
			if !ok {
				set = &layoutSet{name: name, enc: enc}
				sets[name] = set
				order = append(order, name)
			}
			set.keys = append(set.keys, key)
		}
	}
	var conflicts []string
	for _, name := range order {
		moved, err := moveLayoutSet(ctx, from, to, sets[name])
		if err != nil {
			return err
		}
		if !moved {
			conflicts = append(conflicts, name)
		}
	}
	if len(conflicts) > 0 {
		sort.Strings(conflicts)
		return &LayoutConflictError{Names: conflicts}
	}
	return nil
}

// moveLayoutSet moves one credential's records and reports false, leaving
// both stores as they were, when to already holds a record of that name or
// the guarded write did not apply.
func moveLayoutSet(ctx context.Context, from, to Store, set *layoutSet) (bool, error) {
	held, err := destinationHolds(ctx, to, set)
	if err != nil || held {
		return false, err
	}
	guards := make([]kvstore.Guard, 0, len(set.keys))
	writes := make([]kvstore.KV, 0, len(set.keys))
	defer func() {
		for _, w := range writes {
			clear(w.Value)
		}
	}()
	for _, key := range set.keys {
		value, err := from.Get(ctx, key)
		if err != nil {
			return false, err
		}
		guards = append(guards, kvstore.Guard{Key: key})
		writes = append(writes, kvstore.KV{Key: key, Value: value})
	}
	applied, err := to.PutAllIfAllUnchanged(ctx, guards, writes)
	if err != nil || !applied {
		return false, err
	}
	for _, key := range set.keys {
		if err := from.Delete(ctx, key); err != nil {
			return false, err
		}
	}
	return true, nil
}

// destinationHolds reports whether to holds any replicated-layout record of
// the set's credential name, not only the keys the set would write.
func destinationHolds(ctx context.Context, to Store, set *layoutSet) (bool, error) {
	for _, key := range []string{credentialPrefix + set.name, MetaKeyPrefix + set.name, MigrationClaimPrefix + set.name} {
		keys, err := to.List(ctx, key)
		if err != nil {
			return false, err
		}
		if slices.Contains(keys, key) {
			return true, nil
		}
	}
	for _, prefix := range []string{VersionKeyPrefix + set.enc + "/", StagedKeyPrefix + set.enc + "/"} {
		keys, err := to.List(ctx, prefix)
		if err != nil {
			return false, err
		}
		if len(keys) > 0 {
			return true, nil
		}
	}
	return false, nil
}
