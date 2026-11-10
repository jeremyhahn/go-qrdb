// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package credentials

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
)

// legacyCredentialPrefix is the key prefix the barrier strategy kept its
// records under in each node's own seal store, before the records moved to
// the replicated store. Under it the metadata and version records are keyed
// by the name followed by metaSuffix or versionPrefix+n (splitRecordKey);
// the replicated store keys them under prefixes of their own
// (replicatedKey).
const legacyCredentialPrefix = "credentials/" //nolint:gosec // G101: a key prefix, not a credential

// The migration records are keyed by credential name under prefixes of their
// own: the claim naming the copy the first node to migrate wrote, in the
// replicated store, and this node's note that its copy is settled and its old
// records are being deleted, in its own seal store. A credential name may be
// any non-blank string, '#' included, so every key under credentialPrefix or
// legacyCredentialPrefix is a credential's; a key under neither is none.
const (
	MigrationClaimPrefix      = "__sys:credential-migrations/"
	legacyMigrationNotePrefix = "credential-migrations/"
)

// MigrationResolution is how an operator settles a migration conflict.
type MigrationResolution string

const (
	// MigrationImport writes the kept copy's current value as a new
	// version of the replicated record, retaining the version it replaces,
	// then deletes the kept copy.
	MigrationImport MigrationResolution = "import"
	// MigrationDiscard deletes the kept copy and leaves the replicated
	// record as it is.
	MigrationDiscard MigrationResolution = "discard"
)

// WithLegacyStore names the node-local store that holds this node's records
// under the old credentials/ layout. BindStore migrates them into the bound
// store (docs/credentials/architecture.md, "Prefix migration").
func WithLegacyStore(st Store) Option {
	return func(s *CredentialService) { s.legacy = st }
}

// migrationClaim is the record under migrationClaimPrefix+name.
// It holds the digests of the copy that became the replicated record, never
// a value, so a later node can tell whether its copy is the same one and,
// if not, which parts differ.
type migrationClaim struct {
	Digest   string `json:"digest"`
	Value    string `json:"value"`
	Versions string `json:"versions"`
	Policy   string `json:"policy"`
	// Complete is set once every record of the winning copy is written.
	// Until then a node holding the same copy finishes the write.
	Complete bool `json:"complete"`
}

// credentialRecord is one credential's records, keyed by the suffix after
// its name: "" for the current value, metaSuffix, and versionPrefix+n.
type credentialRecord map[string][]byte

// claim computes the digests of r.
func (r credentialRecord) claim() migrationClaim {
	suffixes := make([]string, 0, len(r))
	for k := range r {
		suffixes = append(suffixes, k)
	}
	sort.Strings(suffixes)

	all, versions := sha256.New(), sha256.New()
	for _, k := range suffixes {
		writeField(all, k)
		writeField(all, string(r[k]))
		if strings.HasPrefix(k, versionPrefix) {
			writeField(versions, k)
			writeField(versions, string(r[k]))
		}
	}
	value := sha256.New()
	if v, ok := r[""]; ok {
		writeField(value, string(v))
	}
	policy := sha256.New()
	if raw, ok := r[metaSuffix]; ok {
		var m credentialMeta
		if err := json.Unmarshal(raw, &m); err != nil {
			writeField(policy, string(raw))
		} else {
			hist, _ := json.Marshal(struct {
				Current uint64                `json:"c"`
				History []credentialMetaEntry `json:"h"`
			}{m.CurrentVersion, m.History})
			writeField(versions, string(hist))
			pol, _ := json.Marshal(m.Policy)
			writeField(policy, string(pol))
		}
	}
	return migrationClaim{
		Digest:   hex.EncodeToString(all.Sum(nil)),
		Value:    hex.EncodeToString(value.Sum(nil)),
		Versions: hex.EncodeToString(versions.Sum(nil)),
		Policy:   hex.EncodeToString(policy.Sum(nil)),
	}
}

// writeField writes s length-prefixed, so field boundaries are unambiguous.
func writeField(w interface{ Write([]byte) (int, error) }, s string) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(s)))
	_, _ = w.Write(n[:])
	_, _ = w.Write([]byte(s))
}

// legacyCopy is one credential as this node's legacy store holds it.
type legacyCopy struct {
	record credentialRecord
	// settled reports this node's migration note: the copy was migrated or
	// resolved and only its deletion was interrupted.
	settled bool
}

// BindStore attaches the store the barrier strategy keeps its records in,
// replacing any store the service was built with. A node calls it once the
// system shard's replicated KV exists (lifecycle.BuildServices), with
// lifecycle.NewReplicatedSealBackend over that KV, so every node reads and
// conditionally writes the same records. A nil st is refused with
// ErrNilStore. A manual-strategy service keeps no records and ignores it.
//
// When the service has a legacy store (WithLegacyStore), BindStore then
// migrates this node's old credentials/ records into st (MigrateLegacy). A
// migration that cannot finish, such as one whose legacy store is still
// sealed, leaves the store bound and is logged; the next MigrateLegacy,
// ListMigrationConflicts or ResolveMigrationConflict runs it again, so the
// bind error is only ErrNilStore.
func (s *CredentialService) BindStore(ctx context.Context, st Store) error {
	if err := s.store.bind(st); err != nil {
		return err
	}
	if err := s.MigrateLegacy(ctx); err != nil {
		s.logger.Error("credential migration from the node-local layout did not finish",
			slog.String("error", err.Error()))
	}
	return nil
}

// MigrateLegacy moves this node's records from the legacy store's old
// credentials/ layout to the bound store, per credential name, under
// ruling 279 option A:
//
//   - the first node to migrate a name wins: its copy becomes the
//     replicated record;
//   - a later node whose copy is byte-identical in value, versions and
//     policy deletes its old records;
//   - a later node whose copy differs keeps its old records untouched and
//     reports a CredentialMigrationConflictError to the log, the audit log
//     and ListMigrationConflicts.
//
// It runs once per service and is idempotent: a run interrupted at any
// point and run again reaches the same result. A service with no legacy
// store, or of the manual strategy, has nothing to migrate.
func (s *CredentialService) MigrateLegacy(ctx context.Context) error {
	s.migrateMu.Lock()
	defer s.migrateMu.Unlock()
	return s.migrateLocked(ctx)
}

// migrateLocked runs the migration unless it already ran. Callers hold
// migrateMu.
func (s *CredentialService) migrateLocked(ctx context.Context) error {
	if s.migrated || s.legacy == nil || s.strategy != StrategyBarrier {
		return nil
	}
	copies, err := s.loadLegacy(ctx, "")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(copies))
	for name := range copies {
		names = append(names, name)
	}
	sort.Strings(names)

	conflicts := make(map[string]*CredentialMigrationConflictError)
	for _, name := range names {
		conflict, err := s.migrateOne(ctx, name, copies[name])
		if err != nil {
			return err
		}
		if conflict != nil {
			conflicts[name] = conflict
			s.logger.Warn("credential migration conflict: local copy kept",
				slog.String("name", name),
				slog.String("error", conflict.Error()))
			s.emitAuditOutcome(ctx, "migrate", name, audit.OutcomeFailure, map[string]any{
				"conflict": conflict.Fields(),
			})
			continue
		}
		s.emitAudit(ctx, "migrate", name, nil)
	}
	s.conflicts = conflicts
	s.migrated = true
	return nil
}

// migrateOne settles one name and returns its conflict, if any.
func (s *CredentialService) migrateOne(ctx context.Context, name string, c *legacyCopy) (*CredentialMigrationConflictError, error) {
	if c.settled {
		return nil, s.deleteLegacy(ctx, name, c)
	}
	ours := c.record.claim()
	claim, err := s.loadOrClaim(ctx, name, ours)
	if err != nil {
		return nil, err
	}
	if claim.Digest != ours.Digest {
		return &CredentialMigrationConflictError{
			Name:     name,
			Value:    claim.Value != ours.Value,
			Versions: claim.Versions != ours.Versions,
			Policy:   claim.Policy != ours.Policy,
		}, nil
	}
	if !claim.Complete {
		if err := s.writeWinningCopy(ctx, name, c.record, claim); err != nil {
			return nil, err
		}
	}
	return nil, s.deleteLegacy(ctx, name, c)
}

// loadOrClaim returns the name's migration claim, writing ours when there is
// none. A replicated record written before any node migrated the name was
// first, so the claim then names that record, complete.
func (s *CredentialService) loadOrClaim(ctx context.Context, name string, ours migrationClaim) (migrationClaim, error) {
	key := MigrationClaimPrefix + name
	if claim, ok := s.loadClaim(ctx, key); ok {
		return claim, nil
	}
	existing, err := s.loadReplicated(ctx, name)
	if err != nil {
		return migrationClaim{}, err
	}
	want := ours
	if len(existing) > 0 {
		want = existing.claim()
		want.Complete = true
	}
	raw, err := json.Marshal(want)
	if err != nil {
		return migrationClaim{}, &OperationError{Op: "encode migration claim", Cause: err}
	}
	won, err := s.store.PutIfAbsent(ctx, key, raw)
	if err != nil {
		return migrationClaim{}, &OperationError{Op: "write migration claim", Cause: err}
	}
	if won {
		return want, nil
	}
	claim, ok := s.loadClaim(ctx, key)
	if !ok {
		return migrationClaim{}, &OperationError{Op: "read migration claim", Cause: ErrStoreUnavailable}
	}
	return claim, nil
}

// loadClaim reads and decodes the claim at key.
func (s *CredentialService) loadClaim(ctx context.Context, key string) (migrationClaim, bool) {
	raw, err := s.store.Get(ctx, key)
	if err != nil || len(raw) == 0 {
		return migrationClaim{}, false
	}
	var claim migrationClaim
	if err := json.Unmarshal(raw, &claim); err != nil {
		return migrationClaim{}, false
	}
	return claim, true
}

// writeWinningCopy writes the winning copy's records and marks the claim
// complete. A record already present was written by this copy's earlier,
// interrupted attempt or by a later live write, and is kept.
func (s *CredentialService) writeWinningCopy(ctx context.Context, name string, rec credentialRecord, claim migrationClaim) error {
	suffixes := make([]string, 0, len(rec))
	for k := range rec {
		suffixes = append(suffixes, k)
	}
	// The current value goes last: until it is written no reader sees a
	// credential, only its sidecars. It sorts first, being "".
	sort.Strings(suffixes)
	if len(suffixes) > 0 && suffixes[0] == "" {
		suffixes = append(suffixes[1:], "")
	}
	for _, k := range suffixes {
		if _, err := s.store.PutIfAbsent(ctx, replicatedKey(name, k), rec[k]); err != nil {
			return &OperationError{Op: "write migrated record", Cause: err}
		}
	}
	pending, err := json.Marshal(claim)
	if err != nil {
		return &OperationError{Op: "encode migration claim", Cause: err}
	}
	claim.Complete = true
	done, err := json.Marshal(claim)
	if err != nil {
		return &OperationError{Op: "encode migration claim", Cause: err}
	}
	// A failed swap means another node holding the same copy completed it.
	if _, err := s.store.CompareAndSwap(ctx, MigrationClaimPrefix+name, pending, done); err != nil {
		return &OperationError{Op: "complete migration claim", Cause: err}
	}
	return nil
}

// deleteLegacy deletes this node's old records for name. It writes the
// settled note first and deletes it last, so a run interrupted part-way
// finishes the deletion instead of mistaking what is left for a different
// copy.
func (s *CredentialService) deleteLegacy(ctx context.Context, name string, c *legacyCopy) error {
	note := legacyMigrationNotePrefix + name
	if !c.settled {
		if err := s.legacy.Put(ctx, note, []byte{1}); err != nil {
			return &OperationError{Op: "write migration note", Cause: err}
		}
	}
	for k := range c.record {
		// The value goes last: while a metadata or version record is
		// left, the value that makes it one of name's is too.
		if k == "" {
			continue
		}
		if err := s.legacy.Delete(ctx, legacyCredentialPrefix+name+k); err != nil {
			return &OperationError{Op: "delete legacy record", Cause: err}
		}
	}
	if _, ok := c.record[""]; ok {
		if err := s.legacy.Delete(ctx, legacyCredentialPrefix+name); err != nil {
			return &OperationError{Op: "delete legacy record", Cause: err}
		}
	}
	if err := s.legacy.Delete(ctx, note); err != nil {
		return &OperationError{Op: "delete migration note", Cause: err}
	}
	return nil
}

// loadLegacy reads the legacy store's records, grouped by the name
// splitRecordKey parses from each key. A non-empty only restricts the result
// to that name. A name is held, so that keys ending in a record suffix
// after it are its records, when the listing holds its value, when this
// node's migration note names it (its deletion was interrupted), or when it
// is the name asked for.
func (s *CredentialService) loadLegacy(ctx context.Context, only string) (map[string]*legacyCopy, error) {
	keys, err := s.legacy.List(ctx, legacyCredentialPrefix+only)
	if err != nil {
		return nil, &OperationError{Op: "list legacy records", Cause: err}
	}
	notes, err := s.legacy.List(ctx, legacyMigrationNotePrefix+only)
	if err != nil {
		return nil, &OperationError{Op: "list migration notes", Cause: err}
	}
	held := make(map[string]struct{}, len(keys)+len(notes)+1)
	for _, key := range keys {
		held[strings.TrimPrefix(key, legacyCredentialPrefix)] = struct{}{}
	}
	for _, key := range notes {
		held[strings.TrimPrefix(key, legacyMigrationNotePrefix)] = struct{}{}
	}
	if only != "" {
		held[only] = struct{}{}
	}
	copies := make(map[string]*legacyCopy)
	for _, key := range keys {
		name, suffix := splitRecordKey(strings.TrimPrefix(key, legacyCredentialPrefix), held)
		if only != "" && name != only {
			continue
		}
		c := copies[name]
		if c == nil {
			c = &legacyCopy{record: credentialRecord{}}
			copies[name] = c
		}
		value, err := s.legacy.Get(ctx, key)
		if err != nil {
			return nil, &OperationError{Op: "read legacy record", Cause: err}
		}
		c.record[suffix] = value
	}
	for _, key := range notes {
		name := strings.TrimPrefix(key, legacyMigrationNotePrefix)
		if only != "" && name != only {
			continue
		}
		c := copies[name]
		if c == nil {
			c = &legacyCopy{record: credentialRecord{}}
			copies[name] = c
		}
		c.settled = true
	}
	return copies, nil
}

// loadReplicated reads the bound store's records for name, without its
// migration claim.
func (s *CredentialService) loadReplicated(ctx context.Context, name string) (credentialRecord, error) {
	rec := credentialRecord{}
	// The value and the metadata record are keyed by the name itself; a
	// listing by that prefix also returns longer names, which are other
	// credentials.
	for prefix, part := range map[string]string{credentialPrefix: "", MetaKeyPrefix: metaSuffix} {
		keys, err := s.store.List(ctx, prefix+name)
		if err != nil {
			return nil, &OperationError{Op: "list replicated records", Cause: err}
		}
		for _, key := range keys {
			if key != prefix+name {
				continue
			}
			value, err := s.store.Get(ctx, key)
			if err != nil {
				return nil, &OperationError{Op: "read replicated record", Cause: err}
			}
			rec[part] = value
		}
	}
	prefix := VersionKeyPrefix + encodedName(name) + "/"
	keys, err := s.store.List(ctx, prefix)
	if err != nil {
		return nil, &OperationError{Op: "list replicated records", Cause: err}
	}
	for _, key := range keys {
		value, err := s.store.Get(ctx, key)
		if err != nil {
			return nil, &OperationError{Op: "read replicated record", Cause: err}
		}
		rec[versionPrefix+strings.TrimPrefix(key, prefix)] = value
	}
	return rec, nil
}

// replicatedKey is the bound store's key for the record of name that
// splitRecordKey parsed suffix for: "" is the value, metaSuffix the
// metadata, and versionPrefix+n version n.
func replicatedKey(name, suffix string) string {
	if suffix == metaSuffix {
		return metaKey(name)
	}
	if n, ok := strings.CutPrefix(suffix, versionPrefix); ok {
		if version, err := strconv.ParseUint(n, 10, 64); err == nil {
			return versionKey(name, version)
		}
	}
	return credentialPrefix + name + suffix
}

// splitRecordKey splits a key relative to the legacy prefix into the
// credential name and the record's suffix. The legacy store appended
// metaSuffix or versionPrefix+n to the name, and a name may itself hold a
// '#', so the key is read from its end: it is a metadata or version record
// of <name> only when it ends in exactly metaSuffix or versionPrefix and a
// decimal version, and held, the keys of the same listing, holds <name>'s
// value. Any other key is the value of the credential the whole key names.
func splitRecordKey(rest string, held map[string]struct{}) (name, suffix string) {
	base, ok := strings.CutSuffix(rest, metaSuffix)
	if !ok {
		i := strings.LastIndex(rest, versionPrefix)
		if i < 0 {
			return rest, ""
		}
		if _, err := strconv.ParseUint(rest[i+len(versionPrefix):], 10, 64); err != nil {
			return rest, ""
		}
		base = rest[:i]
	}
	if _, ok := held[base]; !ok {
		return rest, ""
	}
	// base is a credential's value only when it is not itself a record of
	// a shorter name.
	if _, sub := splitRecordKey(base, held); sub != "" {
		return rest, ""
	}
	return base, rest[len(base):]
}

// ListMigrationConflicts returns the unresolved migration conflicts on this
// node, sorted by name. It first finishes a migration that has not run or
// did not finish.
func (s *CredentialService) ListMigrationConflicts(ctx context.Context) ([]CredentialMigrationConflictError, error) {
	s.migrateMu.Lock()
	defer s.migrateMu.Unlock()
	if err := s.migrateLocked(ctx); err != nil {
		return nil, err
	}
	out := make([]CredentialMigrationConflictError, 0, len(s.conflicts))
	for _, c := range s.conflicts {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// migrationResolvers dispatches a MigrationResolution.
var migrationResolvers = map[MigrationResolution]func(*CredentialService, context.Context, string, *legacyCopy) (map[string]any, error){
	MigrationImport:  (*CredentialService).importLegacy,
	MigrationDiscard: (*CredentialService).discardLegacy,
}

// ResolveMigrationConflict settles the named conflict: MigrationImport
// writes the kept copy's current value as a new version of the replicated
// record, retaining the version it replaces, and MigrationDiscard drops the
// kept copy. Either way the kept copy's old records are then deleted, and
// the resolution is audited with the versions it did not carry over.
func (s *CredentialService) ResolveMigrationConflict(ctx context.Context, name string, how MigrationResolution) error {
	if err := validateName(name); err != nil {
		return err
	}
	resolve, ok := migrationResolvers[how]
	if !ok {
		return ErrInvalidMigrationResolution
	}
	s.migrateMu.Lock()
	defer s.migrateMu.Unlock()
	if err := s.migrateLocked(ctx); err != nil {
		return err
	}
	if _, ok := s.conflicts[name]; !ok {
		return ErrMigrationConflictNotFound
	}
	copies, err := s.loadLegacy(ctx, name)
	if err != nil {
		return err
	}
	c := copies[name]
	if c == nil {
		delete(s.conflicts, name)
		return ErrMigrationConflictNotFound
	}
	// A settled copy was resolved by an earlier call whose deletion was
	// interrupted, and what is left of it is not the copy that was decided
	// on: only the deletion runs again.
	attrs := map[string]any{"already_resolved": true}
	if !c.settled {
		if attrs, err = resolve(s, ctx, name, c); err != nil {
			return err
		}
	}
	if err := s.deleteLegacy(ctx, name, c); err != nil {
		return err
	}
	delete(s.conflicts, name)
	attrs["resolution"] = string(how)
	attrs["dropped_records"] = droppedRecords(c.record, how == MigrationImport)
	s.emitAudit(ctx, "migrate_resolve", name, attrs)
	return nil
}

// importLegacy writes the kept copy's current value as a new version.
func (s *CredentialService) importLegacy(ctx context.Context, name string, c *legacyCopy) (map[string]any, error) {
	value, ok := c.record[""]
	if !ok || len(value) == 0 {
		return nil, ErrCredentialNotFound
	}
	digest := c.record.claim().Digest
	meta, err := s.loadBarrierMeta(ctx, name)
	if err != nil {
		return nil, err
	}
	if slices.Contains(meta.Imported, digest) {
		// An earlier resolve committed this copy and failed deleting it.
		// The credential may have been rotated since, so nothing is
		// imported or replaced; only the deletion is left.
		return map[string]any{"already_imported": true}, nil
	}
	res, err := s.rotateBarrier(ctx, name, RotateOptions{
		NewValue:       bytes.Clone(value),
		retainPrevious: true,
		importDigest:   digest,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"previous_version": res.PreviousVersion,
		"new_version":      res.NewVersion,
	}, nil
}

// discardLegacy keeps the replicated record as it is.
func (s *CredentialService) discardLegacy(context.Context, string, *legacyCopy) (map[string]any, error) {
	return map[string]any{}, nil
}

// droppedRecords lists the suffixes of the kept copy's records a resolution
// does not carry into the replicated record ("current" for the value): all
// of them, but for the current value when it was imported.
func droppedRecords(rec credentialRecord, imported bool) []string {
	out := make([]string, 0, len(rec))
	for k := range rec {
		if k == "" {
			if imported {
				continue
			}
			k = "current"
		}
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
