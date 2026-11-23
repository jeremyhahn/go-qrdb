package credentials

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
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
// version, a staged rotation, the migration claims, and the migration
// import records. An import record moves with its credential, so a conflict
// import retried against the destination finds its copy already committed;
// one a deleted credential left moves in a set of its own.
var layoutPrefixes = []string{credentialPrefix, MetaKeyPrefix, VersionKeyPrefix, StagedKeyPrefix, MigrationClaimPrefix, MigrationImportPrefix}

// LayoutMovedPrefix keys the move marker a layout move writes into the
// destination store in the same guarded batch as a credential's records:
// it says the copy there is this source store's, so a pass that stopped
// before the source records were deleted is finished, not reported as a
// conflict. The marker key is LayoutMovedPrefix + <source id> + "/" +
// <name>, and its value the move id of the pass that wrote it. The same
// batch writes the name's fence, LayoutMovedPrefix + "fence/" + <name>,
// which every source's batch requires absent, so two sources never both
// commit a name. A name the destination already held when a pass reached
// it gets a conflict record, LayoutMovedPrefix + "conflict/" + <source id>
// + "/" + <name>, which outlives a later delete of the destination copy:
// the source copy is never moved once it lost, so a deleted credential
// cannot come back from it. The batch also writes the name's settlement
// record, LayoutMovedPrefix + "settled/" + <name>, which every batch
// requires absent and which no pass deletes: the fence and marker go when a
// move finishes, the settlement record stays, so once a name settled no
// source, the same or a later one, moves its copy in again, even after the
// credential was deleted from the destination. No layout
// prefix matches it, and the marker carries no secret. pkg/service
// hard-confines it: a forged marker would make a move pass delete the
// source records without copying them.
const LayoutMovedPrefix = "__sys:credential-layout-moved/"

// LayoutSourcePrefix holds the identity record of a store credentials are
// moved out of; LayoutSourceKey is that record. It is not a layout
// prefix and is never moved. pkg/service hard-confines it: a forged or
// replaced source identity would let a move pass claim another source's
// markers.
const LayoutSourcePrefix = "__sys:credential-layout-source/"

// LayoutSourceKey is the source store's identity record.
const LayoutSourceKey = LayoutSourcePrefix + "id"

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
// the name segment its version, staged and migration import keys carry.
func layoutSetOf(prefix, key string) (name, enc string) {
	rest := strings.TrimPrefix(key, prefix)
	if prefix != VersionKeyPrefix && prefix != StagedKeyPrefix && prefix != MigrationImportPrefix {
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

// layoutMove identifies one move pass: the source store's identity and a
// fresh id for this pass.
type layoutMove struct{ source, id string }

// marker is the key of name's move marker for this pass's source store.
func (m layoutMove) marker(name string) string {
	return LayoutMovedPrefix + m.source + "/" + name
}

// layoutFenceSource is the source segment of the name fences under
// LayoutMovedPrefix. Source identities are hex, so it is never one.
const layoutFenceSource = "fence"

// layoutFence is the key that fences name across sources while a move of
// it is committed and not finished. It holds the moving source's identity.
func layoutFence(name string) string {
	return LayoutMovedPrefix + layoutFenceSource + "/" + name
}

// layoutConflictSource is the source segment of the conflict records: no
// source id is a word.
const layoutConflictSource = "conflict"

// layoutConflictRoot is the prefix of source's conflict records.
func layoutConflictRoot(source string) string {
	return LayoutMovedPrefix + layoutConflictSource + "/" + source + "/"
}

// layoutSettledSource is the source segment of the settlement records: no
// source id is a word.
const layoutSettledSource = "settled"

// layoutSettled keys name's settlement record. The first move of name to
// commit writes it, holding the moving source's identity, in the same
// guarded batch as the records, and no pass ever deletes it: unlike the
// fence and the marker it outlives the move and a later delete of the
// credential from the destination. Every later pass of any source finds it
// (destinationHolds) and every batch requires it absent, so once a name
// settled in the destination no source copy of it is moved there again.
func layoutSettled(name string) string {
	return LayoutMovedPrefix + layoutSettledSource + "/" + name
}

// conflict keys the record that the destination held name when this source's pass
// reached it, so this source's copy of name is never moved.
func (m layoutMove) conflict(name string) string {
	return layoutConflictRoot(m.source) + name
}

// layoutSourceID returns the identity of the store credentials are moved
// out of, writing a fresh one the first time. A concurrent first write
// that wins the race is the identity.
func layoutSourceID(ctx context.Context, from Store) (string, error) {
	held, err := holdsKey(ctx, from, LayoutSourceKey)
	if err != nil {
		return "", err
	}
	if !held {
		id, err := newLayoutID()
		if err != nil {
			return "", err
		}
		if _, err := from.PutIfAbsent(ctx, LayoutSourceKey, []byte(id)); err != nil {
			return "", err
		}
	}
	id, err := from.Get(ctx, LayoutSourceKey)
	if err != nil {
		return "", err
	}
	if len(id) == 0 {
		return "", errors.New("credentials: empty credential store identity")
	}
	src := string(id)
	clear(id)
	return src, nil
}

// newLayoutID returns a random 128-bit id in hex, which never holds '/'.
func newLayoutID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// moveLayout moves every replicated-layout record from from into to, one
// credential name at a time. A name to already holds any record of is not
// moved: both copies stay where they are and the name is reported in a
// *LayoutConflictError once every other name has moved. Every other name's
// records are written in one guarded batch that requires each destination
// key absent and writes the name's move marker with them. Markers are per
// source store: the marker key names from's identity (LayoutSourceKey) and
// its value this pass's move id. Only a batch that applied deletes its
// source records, and the marker goes last: a move cut short before the
// batch copies again next time, and one cut short after it finds this
// source's marker and only deletes the source records left, so they can
// never bring back a credential deleted from to since. Another source's
// marker on a name is an ordinary conflict. A marker of this source whose
// name has no source records left (a pass cut short before deleting it) is
// removed at the end; other sources' markers are left alone. Each value
// read from from is wiped once its write attempt is over.
func moveLayout(ctx context.Context, from, to Store) error {
	src, err := layoutSourceID(ctx, from)
	if err != nil {
		return err
	}
	id, err := newLayoutID()
	if err != nil {
		return err
	}
	move := layoutMove{source: src, id: id}
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
		moved, err := moveLayoutSet(ctx, from, to, move, sets[name])
		if err != nil {
			return err
		}
		if !moved {
			conflicts = append(conflicts, name)
		}
	}
	markerRoot := LayoutMovedPrefix + move.source + "/"
	markers, err := to.List(ctx, markerRoot)
	if err != nil {
		return err
	}
	for _, marker := range markers {
		name := strings.TrimPrefix(marker, markerRoot)
		if _, ok := sets[name]; ok {
			continue
		}
		if err := clearLayoutMarker(ctx, to, move.source, name); err != nil {
			return err
		}
	}
	// A conflict record whose source records are gone has nothing left
	// to hold back.
	conflictRoot := layoutConflictRoot(move.source)
	recorded, err := to.List(ctx, conflictRoot)
	if err != nil {
		return err
	}
	for _, key := range recorded {
		if _, ok := sets[strings.TrimPrefix(key, conflictRoot)]; ok {
			continue
		}
		if err := to.Delete(ctx, key); err != nil {
			return err
		}
	}
	if len(conflicts) > 0 {
		sort.Strings(conflicts)
		return &LayoutConflictError{Names: conflicts}
	}
	return nil
}

// moveLayoutSet moves one credential's records and reports false, leaving
// both stores as they were, when to already holds a record of that name
// without its move marker or the guarded write did not apply. When to
// holds this source's marker, an earlier pass committed the set: its
// remaining source records and then the marker are deleted, and nothing is
// copied. Another source's marker or fence on the name is an ordinary
// conflict (destinationHolds), even once that source's moved records were
// deleted from to.
func moveLayoutSet(ctx context.Context, from, to Store, move layoutMove, set *layoutSet) (bool, error) {
	marker := move.marker(set.name)
	marked, err := holdsKey(ctx, to, marker)
	if err != nil {
		return false, err
	}
	if marked {
		// An earlier pass committed this set into to and stopped before
		// its source records were gone: finish that pass. The source
		// copy is never copied again, so a credential deleted from to
		// since cannot come back.
		return true, finishLayoutSet(ctx, from, to, move, set)
	}
	// A conflict an earlier pass recorded stands even when the
	// destination copy that caused it was deleted since: the source copy
	// is older than that delete and must not bring the credential back.
	lost, err := holdsKey(ctx, to, move.conflict(set.name))
	if err != nil || lost {
		return false, err
	}
	held, err := destinationHolds(ctx, to, move, set)
	if err != nil {
		return false, err
	}
	if held {
		_, err := to.PutIfAbsent(ctx, move.conflict(set.name), []byte(move.id))
		return false, err
	}
	guards := make([]kvstore.Guard, 0, len(set.keys)+3)
	writes := make([]kvstore.KV, 0, len(set.keys)+3)
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
	// The fence is one key per name for every source: of two sources
	// moving the same name, the batch that lands second finds it and
	// writes nothing. The settlement record is the durable form of the
	// fence: it stays once the move finished, so a name that settled here
	// is never moved again from any source, even after its delete.
	fence := layoutFence(set.name)
	settled := layoutSettled(set.name)
	guards = append(guards, kvstore.Guard{Key: marker}, kvstore.Guard{Key: fence}, kvstore.Guard{Key: settled})
	writes = append(writes,
		kvstore.KV{Key: marker, Value: []byte(move.id)},
		kvstore.KV{Key: fence, Value: []byte(move.source)},
		kvstore.KV{Key: settled, Value: []byte(move.source)})
	applied, err := to.PutAllIfAllUnchanged(ctx, guards, writes)
	if err != nil {
		return false, err
	}
	if !applied {
		// A refused guard is a record of the name that landed in to
		// since the probe: the same conflict, recorded the same way.
		_, err := to.PutIfAbsent(ctx, move.conflict(set.name), []byte(move.id))
		return false, err
	}
	return true, finishLayoutSet(ctx, from, to, move, set)
}

// finishLayoutSet deletes the source records of a set whose copy, with its
// move marker, is committed in to, and then the marker. A source key that
// is already gone is not an error.
func finishLayoutSet(ctx context.Context, from, to Store, move layoutMove, set *layoutSet) error {
	for _, key := range set.keys {
		present, err := holdsKey(ctx, from, key)
		if err != nil {
			return err
		}
		if !present {
			continue
		}
		if err := from.Delete(ctx, key); err != nil {
			return err
		}
	}
	return clearLayoutMarker(ctx, to, move.source, set.name)
}

// clearLayoutMarker ends source's move of name in to: it lifts the name's
// fence when source holds it, and then deletes source's marker. The fence
// goes first, so a stop between the two leaves the marker, which still
// holds the name against other sources (destinationHolds).
func clearLayoutMarker(ctx context.Context, to Store, source, name string) error {
	fence := layoutFence(name)
	fenced, err := holdsKey(ctx, to, fence)
	if err != nil {
		return err
	}
	if fenced {
		holder, err := to.Get(ctx, fence)
		if err != nil {
			return err
		}
		ours := string(holder) == source
		clear(holder)
		if ours {
			if err := to.Delete(ctx, fence); err != nil {
				return err
			}
		}
	}
	return to.Delete(ctx, LayoutMovedPrefix+source+"/"+name)
}

// holdsKey reports whether st holds exactly key.
func holdsKey(ctx context.Context, st Store, key string) (bool, error) {
	keys, err := st.List(ctx, key)
	if err != nil {
		return false, err
	}
	return slices.Contains(keys, key), nil
}

// destinationHolds reports whether to holds any replicated-layout record of
// the set's credential name, not only the keys the set would write, or
// another source's move marker or fence for it, or the name's settlement
// record.
func destinationHolds(ctx context.Context, to Store, move layoutMove, set *layoutSet) (bool, error) {
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
	// A move of the same name from another source that committed and has
	// not finished holds the name, even when the credential it moved was
	// deleted since: this source's copy is older than that delete and
	// must not bring the credential back. Its fence is one such marker,
	// and so is the name's settlement record, which no pass deletes: a
	// name any source's move committed stays held after that move
	// finished and after the credential was deleted.
	keys, err := to.List(ctx, LayoutMovedPrefix)
	if err != nil {
		return false, err
	}
	for _, key := range keys {
		src, name, ok := strings.Cut(strings.TrimPrefix(key, LayoutMovedPrefix), "/")
		if ok && name == set.name && src != move.source && src != layoutConflictSource {
			return true, nil
		}
	}
	return false, nil
}
