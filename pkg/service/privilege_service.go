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

package service

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
)

// PrivilegeServicer manages one database's SQL table-privilege catalog: the
// record of which grantee may touch which table and which column. See the
// doc block at the top of privilege_types.go for how it relates to the
// service RBAC control plane and for the activation-latch model.
type PrivilegeServicer interface {
	// Grant adds privileges to a grantee's record. The first successful
	// call against a database also arms its activation latch, after which
	// the database is default-deny for every session.
	Grant(ctx context.Context, req *GrantPrivilegesRequest) error

	// Revoke removes privileges from a grantee's record. It never clears
	// the activation latch: a database's privilege posture is monotonic,
	// so revoking the last privilege leaves the database armed and empty,
	// not unrestricted again.
	Revoke(ctx context.Context, req *RevokePrivilegesRequest) error

	// Privileges returns a grantee's current record, or an empty record
	// when the grantee has none. It performs fresh linearizable reads on
	// every call; the caller-side snapshot cache lives with the enforcer,
	// which is the only component on a latency-sensitive path.
	Privileges(ctx context.Context, database, grantee string) (*PrivilegeSet, error)

	// Grantees returns the names of every grantee holding a record in the
	// database, sorted.
	Grantees(ctx context.Context, database string) ([]string, error)

	// Resolve returns the authorization state of one (database, grantee)
	// pair, reading only what the caller does not already hold. It is the
	// enforcement path's single entry point; see ResolvePrivilegesRequest.
	Resolve(ctx context.Context, req *ResolvePrivilegesRequest) (*PrivilegeResolution, error)
}

// privilegeKVService is the narrow KV interface PrivilegeService needs for
// reads: the latch and epoch counters, a grantee's record, and the
// enumeration SHOW GRANTS and Grantees are built on.
type privilegeKVService interface {
	Get(ctx context.Context, key string) ([]byte, error)
	List(ctx context.Context, prefix string) ([]string, error)
}

// privilegeMVCCService is the narrow MVCC read interface PrivilegeService
// needs to observe each key's ModRevision before proposing a
// compare-and-swap against it.
type privilegeMVCCService interface {
	GetRevision(ctx context.Context, key string) (*KeyValue, error)
}

// PrivilegeService implements PrivilegeServicer over a KV read interface
// (for the enforcement path and enumeration), an MVCC read interface (for
// compare-and-swap preconditions), and a TransactionServicer (for atomic
// mutation).
//
// Every mutation goes through exactly one write path, mutate: a single
// TransactionServicer.Execute whose compares pin the observed ModRevision of
// all three keys it touches — the database latch, the grantee's record, and
// the grantee's epoch — and whose ops advance all three together. There is
// deliberately no separate create path: arming the latch and writing the
// first record must be one atomic step, so a PutIfAbsent that could commit
// on its own would be a second, weaker write path for the same state. A
// record that does not yet exist is claimed with a ModRevision-zero compare,
// the same "absent" encoding SequenceService and TableService.RenameTable
// already use.
//
// No caching. Reads are linearizable through Raft on every call, so a GRANT
// committed on the leader is visible to the very next Resolve issued against
// any follower. The enforcement-path snapshot cache lives in the SQL
// authorization handler, gated on the generation counter this service
// maintains for exactly that purpose.
type PrivilegeService struct {
	kv   privilegeKVService
	mvcc privilegeMVCCService
	txn  TransactionServicer
}

// Compile-time interface check.
var _ PrivilegeServicer = (*PrivilegeService)(nil)

// NewPrivilegeService creates a PrivilegeServicer backed by the given KV
// read interface, MVCC read interface, and TransactionServicer.
func NewPrivilegeService(kv privilegeKVService, mvcc privilegeMVCCService, txn TransactionServicer) *PrivilegeService {
	return &PrivilegeService{kv: kv, mvcc: mvcc, txn: txn}
}

// Grant adds privileges to a grantee's record, arming the database's
// activation latch on the first successful mutation.
func (s *PrivilegeService) Grant(ctx context.Context, req *GrantPrivilegesRequest) error {
	if req == nil {
		return &PrivilegeNoGrantsError{}
	}
	if err := validatePrivilegeGrants(req.Database, req.Grantee, req.Grants); err != nil {
		return err
	}
	return s.mutate(ctx, req.Database, req.Grantee, func(set *PrivilegeSet) {
		for _, g := range req.Grants {
			set.grant(g)
		}
	})
}

// Revoke removes privileges from a grantee's record. The activation latch is
// untouched, so a database that has ever been granted stays default-deny.
func (s *PrivilegeService) Revoke(ctx context.Context, req *RevokePrivilegesRequest) error {
	if req == nil {
		return &PrivilegeNoGrantsError{}
	}
	if err := validatePrivilegeGrants(req.Database, req.Grantee, req.Revokes); err != nil {
		return err
	}
	return s.mutate(ctx, req.Database, req.Grantee, func(set *PrivilegeSet) {
		for _, g := range req.Revokes {
			set.revoke(g)
		}
	})
}

// Privileges returns a grantee's current record, or an empty record when the
// grantee holds none.
func (s *PrivilegeService) Privileges(ctx context.Context, database, grantee string) (*PrivilegeSet, error) {
	if err := validatePrivilegeRef(database, grantee); err != nil {
		return nil, err
	}
	set, err := s.readSet(ctx, database, grantee)
	if err != nil {
		return nil, err
	}
	return set, nil
}

// Grantees returns the names of every grantee holding a record in database,
// sorted. The catalog prefix also contains the database's latch and each
// grantee's epoch key, which are filtered out.
func (s *PrivilegeService) Grantees(ctx context.Context, database string) ([]string, error) {
	if database == "" {
		return nil, &PrivilegeEmptyDatabaseError{}
	}

	prefix := privilegeScanPrefix(database)
	keys, err := s.kv.List(ctx, prefix)
	if err != nil {
		return nil, &PrivilegeMetadataError{Op: "list", Err: err}
	}

	names := make([]string, 0, len(keys))
	for _, key := range keys {
		name := strings.TrimPrefix(key, prefix)
		if name == "" || name == privilegeLatchSuffix || strings.HasSuffix(name, privilegeEpochSuffix) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// Resolve returns the authorization state of one (database, grantee) pair,
// performing the fewest reads the caller's stated knowledge allows:
//
//	1 read  — the database is unrestricted, or the caller's snapshot is
//	          still current at the database's catalog generation.
//	2 reads — the generation moved because some OTHER grantee changed;
//	          this grantee's own epoch proves its record did not.
//	3 reads — this grantee's record actually changed and is re-read.
//
// The reads are issued generation-first, so the record can never be older
// than the generation it is cached under: a snapshot validated on a stale
// generation is only ever re-resolved, never trusted.
func (s *PrivilegeService) Resolve(ctx context.Context, req *ResolvePrivilegesRequest) (*PrivilegeResolution, error) {
	if req == nil {
		return nil, &PrivilegeEmptyDatabaseError{}
	}
	if err := validatePrivilegeRef(req.Database, req.Grantee); err != nil {
		return nil, err
	}

	res := &PrivilegeResolution{Database: req.Database, Grantee: req.Grantee}

	generation, err := s.readCounter(ctx, privilegeLatchKey(req.Database))
	if err != nil {
		return nil, err
	}
	if generation == 0 {
		return res, nil
	}
	res.Latched = true
	res.Generation = generation

	if req.KnownGeneration == generation {
		res.Epoch = req.KnownEpoch
		res.Unchanged = true
		return res, nil
	}

	epoch, err := s.readCounter(ctx, privilegeEpochKey(req.Database, req.Grantee))
	if err != nil {
		return nil, err
	}
	res.Epoch = epoch

	if req.KnownGeneration != 0 && req.KnownEpoch == epoch {
		res.Unchanged = true
		return res, nil
	}

	set, err := s.readSet(ctx, req.Database, req.Grantee)
	if err != nil {
		return nil, err
	}
	res.Set = set
	return res, nil
}

// mutate applies apply to a grantee's record and commits it together with an
// advance of both the database's catalog generation and the grantee's epoch,
// as a single compare-and-swap over all three keys.
//
// apply is invoked fresh on every attempt against a freshly read record, so
// a retry after a lost race composes with the winner's change rather than
// overwriting it. Retries use the same bounded jittered backoff the table
// catalog uses, and only for a genuine server-observed conflict.
func (s *PrivilegeService) mutate(ctx context.Context, database, grantee string, apply func(*PrivilegeSet)) error {
	latchKey := privilegeLatchKey(database)
	recordKey := privilegeGranteeKey(database, grantee)
	epochKey := privilegeEpochKey(database, grantee)

	err := doWithCatalogRetry(ctx, func(ctx context.Context) error {
		generation, latchRev, err := s.readCounterRevision(ctx, latchKey)
		if err != nil {
			return err
		}
		epoch, epochRev, err := s.readCounterRevision(ctx, epochKey)
		if err != nil {
			return err
		}
		set, recordRev, err := s.readSetRevision(ctx, database, grantee)
		if err != nil {
			return err
		}

		apply(set)
		set.UpdatedAt = time.Now().UTC()

		recordOp := Op{Type: OpDelete, Key: []byte(recordKey)}
		if !set.IsEmpty() {
			data, merr := json.Marshal(set)
			if merr != nil {
				return &PrivilegeMetadataError{Op: "marshal", Err: merr}
			}
			recordOp = Op{Type: OpPut, Key: []byte(recordKey), Value: data}
		}

		compares := []Compare{
			{Key: []byte(latchKey), Target: CompareModRevision, Result: CompareEqual, ModRevision: latchRev},
			{Key: []byte(recordKey), Target: CompareModRevision, Result: CompareEqual, ModRevision: recordRev},
			{Key: []byte(epochKey), Target: CompareModRevision, Result: CompareEqual, ModRevision: epochRev},
		}
		ops := []Op{
			{Type: OpPut, Key: []byte(latchKey), Value: encodeSequenceCounter(generation + 1)},
			recordOp,
			{Type: OpPut, Key: []byte(epochKey), Value: encodeSequenceCounter(epoch + 1)},
		}

		resp, terr := s.txn.Execute(ctx, compares, ops, nil)
		if terr != nil {
			return terr
		}
		if !resp.Succeeded {
			return &TransactionConflictError{Key: recordKey}
		}
		return nil
	})
	if err == nil {
		return nil
	}

	var conflict *TransactionConflictError
	if errors.As(err, &conflict) {
		return &PrivilegeConflictError{Database: database, Grantee: grantee, Err: conflict}
	}
	return err
}

// readCounter reads an 8-byte big-endian counter, returning 0 when the key
// has never been written. It shares encodeSequenceCounter/
// decodeSequenceCounter with the sequence catalog rather than repeating the
// encoding.
func (s *PrivilegeService) readCounter(ctx context.Context, key string) (uint64, error) {
	data, err := s.kv.Get(ctx, key)
	if err != nil {
		if isKeyNotFound(err) {
			return 0, nil
		}
		return 0, &PrivilegeMetadataError{Op: "read", Err: err}
	}
	return decodeSequenceCounter(data), nil
}

// readCounterRevision reads a counter together with its ModRevision,
// reporting (0, 0) for a key that has never been written — the same absent
// encoding the compare-and-swap in mutate pins against.
func (s *PrivilegeService) readCounterRevision(ctx context.Context, key string) (uint64, int64, error) {
	kv, err := s.mvcc.GetRevision(ctx, key)
	if err != nil {
		if isKeyNotFound(err) {
			return 0, 0, nil
		}
		return 0, 0, &PrivilegeMetadataError{Op: "read", Err: err}
	}
	return decodeSequenceCounter(kv.Value), kv.ModRevision, nil
}

// readSet reads a grantee's record, returning an empty record when the
// grantee has none.
func (s *PrivilegeService) readSet(ctx context.Context, database, grantee string) (*PrivilegeSet, error) {
	data, err := s.kv.Get(ctx, privilegeGranteeKey(database, grantee))
	if err != nil {
		if isKeyNotFound(err) {
			return newPrivilegeSet(database, grantee), nil
		}
		return nil, &PrivilegeMetadataError{Op: "read", Err: err}
	}
	return decodePrivilegeSet(data, database, grantee)
}

// readSetRevision reads a grantee's record together with its ModRevision,
// reporting an empty record at revision 0 when the grantee has none.
func (s *PrivilegeService) readSetRevision(ctx context.Context, database, grantee string) (*PrivilegeSet, int64, error) {
	kv, err := s.mvcc.GetRevision(ctx, privilegeGranteeKey(database, grantee))
	if err != nil {
		if isKeyNotFound(err) {
			return newPrivilegeSet(database, grantee), 0, nil
		}
		return nil, 0, &PrivilegeMetadataError{Op: "read", Err: err}
	}
	set, derr := decodePrivilegeSet(kv.Value, database, grantee)
	if derr != nil {
		return nil, 0, derr
	}
	return set, kv.ModRevision, nil
}

// decodePrivilegeSet decodes a stored record, restoring the database and
// grantee identity fields from the key the record was found under so that a
// record can never disagree with its own location.
func decodePrivilegeSet(data []byte, database, grantee string) (*PrivilegeSet, error) {
	if len(data) == 0 {
		return newPrivilegeSet(database, grantee), nil
	}
	var set PrivilegeSet
	if err := json.Unmarshal(data, &set); err != nil {
		return nil, &PrivilegeMetadataError{Op: "unmarshal", Err: err}
	}
	set.Database = database
	set.Grantee = grantee
	return &set, nil
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

// validatePrivilegeRef validates that database is non-empty and grantee is
// usable as a catalog key.
func validatePrivilegeRef(database, grantee string) error {
	if database == "" {
		return &PrivilegeEmptyDatabaseError{}
	}
	return ValidateGrantee(grantee)
}

// ValidateGrantee validates that a grantee name can be used as a catalog key
// without colliding with the catalog's own reserved keys.
//
// The "/" ban is what keeps the key space unambiguous: a grantee containing
// one could otherwise be mistaken for another grantee's epoch key, or escape
// the database's prefix entirely.
//
// It is exported so that the SQL layer can refuse an unusable grantee while
// a GRANT statement is still being planned, against these rules rather than
// a second copy of them.
func ValidateGrantee(grantee string) error {
	switch {
	case grantee == "":
		return &PrivilegeInvalidGranteeError{Grantee: grantee, Reason: "must not be empty"}
	case strings.Contains(grantee, "/"):
		return &PrivilegeInvalidGranteeError{Grantee: grantee, Reason: `must not contain "/"`}
	case strings.HasPrefix(grantee, "__"):
		return &PrivilegeInvalidGranteeError{Grantee: grantee, Reason: `must not start with the reserved prefix "__"`}
	default:
		return nil
	}
}

// validatePrivilegeGrants validates a grant or revoke request: the database
// and grantee references, a non-empty grant list, and each grant's tier.
func validatePrivilegeGrants(database, grantee string, grants []PrivilegeGrant) error {
	if err := validatePrivilegeRef(database, grantee); err != nil {
		return err
	}
	if len(grants) == 0 {
		return &PrivilegeNoGrantsError{}
	}
	for _, g := range grants {
		if g.Table == "" && g.Column != "" {
			return &PrivilegeColumnWithoutTableError{Column: g.Column}
		}
	}
	return nil
}
