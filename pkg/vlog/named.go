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

package vlog

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/transparency-dev/merkle/compact"
	"github.com/transparency-dev/merkle/rfc6962"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// HashSHA256 is the one tree hash a log may name: RFC 9162 / RFC 6962 tree
// hashing over SHA-256, with the 0x00 leaf and 0x01 node prefixes.
const HashSHA256 = "sha256"

// MaxAppendPerCommand caps how many entries one append stages, as
// MaxPrunePerCommand caps a prune and an ExpireBodies call.
const MaxAppendPerCommand = 10000

// hasher is the tree hasher every log uses; factory builds compact ranges
// over it. Both are the merkle library's: go-qrdb hashes nothing itself.
var (
	hasher  = rfc6962.DefaultHasher
	factory = &compact.RangeFactory{Hash: hasher.HashChildren}
)

// LogID names one log: the tenant that owns it, the database it lives in and
// its name within that database.
type LogID struct {
	TenantID string
	Database string
	Log      string
}

// Validate refuses a name that would not address exactly one log: any of the
// three containing '/', which ValidateTenantID refuses for the audit table's
// tenant segment for the same reason, or an empty database or log name.
func (id LogID) Validate() error {
	if strings.Contains(id.TenantID, "/") {
		return &InvalidLogNameError{Field: "tenant", Name: id.TenantID}
	}
	if id.Database == "" || strings.Contains(id.Database, "/") {
		return &InvalidLogNameError{Field: "database", Name: id.Database}
	}
	if id.Log == "" || strings.Contains(id.Log, "/") {
		return &InvalidLogNameError{Field: "log", Name: id.Log}
	}
	return nil
}

// String returns id as tenant/database/log.
func (id LogID) String() string {
	return id.TenantID + "/" + id.Database + "/" + id.Log
}

// Descriptor is what a log's owner sets at create, stored as the log's "d"
// record.
type Descriptor struct {
	// Origin is the log's identity, the first line of every checkpoint for
	// it: non-empty, valid UTF-8, no control characters.
	Origin string `json:"origin"`

	// Hash is the tree hash; HashSHA256 is the only one.
	Hash string `json:"hash"`

	// VerifierKeys are the keys a checkpoint's signature is checked against,
	// opaque to go-qrdb.
	VerifierKeys []string `json:"verifier_keys,omitempty"`

	// AllowExpireBodies permits ExpireBodies on the log.
	AllowExpireBodies bool `json:"allow_expire_bodies,omitempty"`

	// Keys are the versions of the key that signs the log's heads, in
	// version order from FirstKeyVersion; the last is current, and a head
	// verifies against the key its KeyVersion names. Create records the
	// first, RotateKey appends the next. A log without keys is not signed
	// by the service.
	Keys []HeadKey `json:"keys,omitempty"`

	// TreeStart is S₀, the audit sequence number of the audit instance's
	// leaf 0: the first audit event appended after featuregate.VerifiableLog
	// is enabled for its group (docs/databases/architecture.md §7). Leaf i
	// is the audit event at sequence TreeStart+i. Zero, and absent, on
	// every other log.
	TreeStart uint64 `json:"tree_start,omitempty"`

	// Audit marks the audit instance: only AuditDescriptor sets it. A log
	// is the instance when IsAuditLog names it and its descriptor has
	// Audit; a log created under the audit name before the instance
	// existed has no Audit, keeps its own entries, and is never
	// reinterpreted (docs/databases/architecture.md §7).
	Audit bool `json:"audit,omitempty"`
}

// Key returns the log's key at version.
func (d Descriptor) Key(version uint32) (HeadKey, bool) {
	if version == 0 || uint64(version) > uint64(len(d.Keys)) {
		return HeadKey{}, false
	}
	return d.Keys[version-1], true
}

// CurrentKey returns the key that signs the log's new heads.
func (d Descriptor) CurrentKey() (HeadKey, bool) {
	return d.Key(uint32(len(d.Keys)))
}

// validate refuses a descriptor Create cannot store.
func (d Descriptor) validate() error {
	if d.Origin == "" || !utf8.ValidString(d.Origin) || strings.ContainsFunc(d.Origin, unicode.IsControl) {
		return &InvalidDescriptorError{Reason: "origin must be non-empty UTF-8 without control characters"}
	}
	if d.Hash != HashSHA256 {
		return &InvalidDescriptorError{Reason: "unsupported hash " + d.Hash}
	}
	for i, k := range d.Keys {
		if k.Version != FirstKeyVersion+uint32(i) {
			return &InvalidDescriptorError{Reason: "key " + strconv.Itoa(i) + " has version " + KeyRef(k.Version)}
		}
		if err := k.validate(); err != nil {
			return &InvalidDescriptorError{Reason: err.Error()}
		}
	}
	return nil
}

// logState is the in-memory half of one log's head record: size is the
// number of entries, lo the lowest index whose body has not expired, hashes
// the compact range of [0, size), and allowExpire the descriptor's flag.
type logState struct {
	size        uint64
	lo          uint64
	hashes      [][]byte
	allowExpire bool
	// treeStart is the descriptor's TreeStart: the audit sequence of leaf 0
	// on an audit instance, zero on every other log.
	treeStart uint64
	// audit is the descriptor's Audit: the log is its tenant's audit
	// instance.
	audit bool
}

// getFunc reads one record, answering nil, nil for an absent key as
// storage.Reader.Get does.
type getFunc func(key []byte) ([]byte, error)

// restoreLogs reads every named log's descriptor and head from r. It visits
// each log once, seeking past the log's entries and nodes rather than
// scanning them, so its cost is in the number of logs, not their size.
func restoreLogs(r storage.Reader) (map[LogID]logState, error) {
	logs := make(map[LogID]logState)
	it := r.NewIterator(&storage.IteratorOptions{
		LowerBound: []byte(LogTablePrefix),
		UpperBound: prefixEnd([]byte(LogTablePrefix)),
	})
	for ok := it.SeekToFirst(); ok && it.Valid(); {
		// Cloned for the same reason Restore captures its key: the slice
		// Key() returns is not the caller's past the next iterator call.
		key := bytes.Clone(it.Key())
		id, prefix, parsed := parseLogPrefix(key)
		if !parsed {
			_ = it.Close()
			return nil, &CorruptTableError{Key: string(key), Reason: "key outside any log's prefix"}
		}
		st, err := readLogState(r.Get, id)
		if err != nil {
			_ = it.Close()
			return nil, err
		}
		logs[id] = st
		ok = it.Seek(prefixEnd(prefix))
	}
	if err := it.Error(); err != nil {
		_ = it.Close()
		return nil, &TableReadError{Err: err}
	}
	if err := it.Close(); err != nil {
		return nil, &TableReadError{Err: err}
	}
	return logs, nil
}

// readLogState reads id's descriptor and head records. Create stages both,
// so a log with either missing is corrupt.
func readLogState(get getFunc, id LogID) (logState, error) {
	dk := descriptorKey(id)
	raw, err := get(dk)
	if err != nil {
		return logState{}, &TableReadError{Err: err}
	}
	var desc Descriptor
	if raw == nil || json.Unmarshal(raw, &desc) != nil {
		return logState{}, &CorruptTableError{Key: string(dk), Reason: "missing or malformed descriptor"}
	}
	size, lo, hashes, err := readLogHead(get, id)
	if missing := (*LogNotFoundError)(nil); errors.As(err, &missing) {
		return logState{}, &CorruptTableError{Key: string(logHeadKey(id)), Reason: "descriptor without a head"}
	}
	if err != nil {
		return logState{}, err
	}
	return logState{size: size, lo: lo, hashes: hashes, allowExpire: desc.AllowExpireBodies, treeStart: desc.TreeStart, audit: desc.Audit}, nil
}

// readLogHead reads and decodes id's head record, answering
// *LogNotFoundError when there is none.
func readLogHead(get getFunc, id LogID) (size, lo uint64, hashes [][]byte, err error) {
	hk := logHeadKey(id)
	raw, err := get(hk)
	if err != nil {
		return 0, 0, nil, &TableReadError{Err: err}
	}
	if raw == nil {
		return 0, 0, nil, &LogNotFoundError{ID: id}
	}
	size, lo, hashes, ok := decodeLogHead(raw)
	if !ok {
		return 0, 0, nil, &CorruptTableError{Key: string(hk), Reason: "malformed head record"}
	}
	return size, lo, hashes, nil
}

// readHash reads the tree node at key, which must be stored: every perfect
// subtree a proof or a root names was completed by an earlier append.
func readHash(get getFunc, key []byte) ([]byte, error) {
	v, err := get(key)
	if err != nil {
		return nil, &TableReadError{Err: err}
	}
	if len(v) != hashSize {
		return nil, &CorruptTableError{Key: string(key), Reason: "missing or malformed tree node"}
	}
	return v, nil
}

// readNodes reads the hashes of the tree nodes ids names, in order.
func readNodes(get getFunc, id LogID, ids []compact.NodeID) ([][]byte, error) {
	hashes := make([][]byte, len(ids))
	for i, nid := range ids {
		h, err := readHash(get, nodeKey(id, nid.Level, nid.Index))
		if err != nil {
			return nil, err
		}
		hashes[i] = h
	}
	return hashes, nil
}

// rangeRoot returns the root of id's tree of size whose compact range is
// hashes. The range is the head's own or one read node by node from the IDs
// compact.RangeNodes names, so it always has the shape size requires.
func rangeRoot(id LogID, size uint64, hashes [][]byte) ([]byte, error) {
	if size == 0 {
		return hasher.EmptyRoot(), nil
	}
	rng, err := factory.NewRange(0, size, hashes)
	if err != nil {
		return nil, &CorruptTableError{Key: string(logHeadKey(id)), Reason: err.Error()}
	}
	root, err := rng.GetRootHash(nil)
	if err != nil {
		return nil, &CorruptTableError{Key: string(logHeadKey(id)), Reason: err.Error()}
	}
	return root, nil
}

// rootAt returns the root of id's tree at size, from the stored perfect
// subtrees covering [0, size).
func rootAt(get getFunc, id LogID, size uint64) ([]byte, error) {
	hashes, err := readNodes(get, id, compact.RangeNodes(0, size, nil))
	if err != nil {
		return nil, err
	}
	return rangeRoot(id, size, hashes)
}

// BeginUpdate drops the tree nodes and checkpoints staged by the previous
// Update call. A state machine calls it at the start of every Update call,
// once that call's predecessor has committed, so apply reads through exactly
// the records the current call has staged and not yet committed.
func (c *Chain) BeginUpdate() {
	c.mu.Lock()
	clear(c.staged)
	c.mu.Unlock()
}

// stagedGet returns a getFunc that reads the current call's staged records
// before r.
func (c *Chain) stagedGet(r storage.Reader) getFunc {
	return func(key []byte) ([]byte, error) {
		if v, ok := c.staged[string(key)]; ok {
			return v, nil
		}
		return r.Get(key)
	}
}

// Create stages a new log's descriptor and empty head into w. It refuses an
// invalid name or descriptor, a descriptor recording more than the first key,
// and a log that already exists. A key the descriptor records is held by its
// provider outside go-qrdb; CreateWithKey stores the software provider's.
func (c *Chain) Create(w Writer, id LogID, desc Descriptor) error {
	return c.create(w, id, desc, nil)
}

// CreateWithKey stages a new log as Create does, and its head-signing key as
// the k/<version> record beside the descriptor, in the same writes: the key
// and the log exist together or not at all. The descriptor must record
// exactly that key, which must be Ed25519 or ECDSA P-256.
func (c *Chain) CreateWithKey(w Writer, id LogID, desc Descriptor, key SigningKey) error {
	if len(desc.Keys) != 1 {
		return &InvalidDescriptorError{Reason: "a log created with its key records exactly that key, not " + strconv.Itoa(len(desc.Keys))}
	}
	if err := desc.Keys[0].matches(key); err != nil {
		return err
	}
	return c.create(w, id, desc, &key)
}

// create stages the descriptor, the empty head and, when key is not nil, the
// signing key.
func (c *Chain) create(w Writer, id LogID, desc Descriptor, key *SigningKey) error {
	if err := id.Validate(); err != nil {
		return err
	}
	if err := desc.validate(); err != nil {
		return err
	}
	if len(desc.Keys) > 1 {
		return &InvalidDescriptorError{Reason: "a log is created with at most its first key"}
	}
	if desc.TreeStart != 0 {
		return &InvalidDescriptorError{Reason: "tree_start is recorded only by the audit instance's first event"}
	}
	// A struct of strings, slices, integers and a bool always marshals, and
	// always to the same bytes.
	body, _ := json.Marshal(desc)

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.auditInstanceLocked(id) {
		return &ReservedLogError{ID: id}
	}
	if _, ok := c.logs[id]; ok {
		return &LogExistsError{ID: id}
	}
	if err := w.Set(descriptorKey(id), body); err != nil {
		return &TableWriteError{Err: err}
	}
	if err := w.Set(logHeadKey(id), encodeLogHead(0, 0, nil)); err != nil {
		return &TableWriteError{Err: err}
	}
	if key != nil {
		if err := w.Set(keyKey(id, key.Version), key.PKCS8); err != nil {
			return &TableWriteError{Err: err}
		}
	}
	c.staged[string(descriptorKey(id))] = body
	c.logs[id] = logState{allowExpire: desc.AllowExpireBodies}
	return nil
}

// RotateKey stages next as id's current head-signing key: the descriptor
// with next appended and, when record is not nil, the software provider's
// k/<version> record, in the same writes. next must be the version after the
// log's current key, so of two rotations racing from the same key the second
// to apply is refused with *KeyVersionConflictError. Earlier keys stay in the
// descriptor, and their records in the table, so every head already signed
// still verifies. The descriptor is read from r through the records the
// current Update call has staged.
func (c *Chain) RotateKey(r storage.Reader, w Writer, id LogID, next HeadKey, record *SigningKey) error {
	if err := id.Validate(); err != nil {
		return err
	}
	if err := next.validate(); err != nil {
		return err
	}
	if record != nil {
		if err := next.matches(*record); err != nil {
			return err
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if _, ok := c.logs[id]; !ok {
		return &LogNotFoundError{ID: id}
	}
	desc, err := readDescriptor(c.stagedGet(r), id)
	if err != nil {
		return err
	}
	if want := FirstKeyVersion + uint32(len(desc.Keys)); next.Version != want {
		return &KeyVersionConflictError{ID: id, Version: next.Version, Want: want}
	}
	desc.Keys = append(desc.Keys, next)
	body, _ := json.Marshal(desc)
	dk := descriptorKey(id)
	if err := w.Set(dk, body); err != nil {
		return &TableWriteError{Err: err}
	}
	if record != nil {
		if err := w.Set(keyKey(id, record.Version), record.PKCS8); err != nil {
			return &TableWriteError{Err: err}
		}
	}
	c.staged[string(dk)] = body
	return nil
}

// AppendEntries assigns entries the next indices of id from the Chain's
// in-memory head, and stages each entry, its leaf hash, every perfect
// subtree node the append completes and the new head into w. It returns the
// first assigned index. The leaf and node hashes come from the merkle
// library's compact range, which reports each node as the append completes
// it; nothing is read from storage.
func (c *Chain) AppendEntries(w Writer, id LogID, entries [][]byte) (first uint64, err error) {
	if err := id.Validate(); err != nil {
		return 0, err
	}
	if len(entries) == 0 || len(entries) > MaxAppendPerCommand {
		return 0, &AppendBatchError{Count: len(entries)}
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.auditInstanceLocked(id) {
		return 0, &ReservedLogError{ID: id}
	}
	st, ok := c.logs[id]
	if !ok {
		return 0, &LogNotFoundError{ID: id}
	}
	for i, e := range entries {
		if err := w.Set(entryKey(id, st.size+uint64(i)), e); err != nil {
			return 0, &TableWriteError{Err: err}
		}
	}
	next, staged, err := appendLeaves(w, id, st, entries)
	if err != nil {
		return 0, err
	}
	maps.Copy(c.staged, staged)
	c.logs[id] = next
	return st.size, nil
}

// appendLeaves stages leaves as the next leaves of id's tree, from st: every
// tree node they complete and the new head. It returns the state to install
// and the staged nodes by key, leaving st and c untouched, so a failed append
// changes nothing in memory. The leaves' bodies are the caller's to store.
func appendLeaves(w Writer, id LogID, st logState, leaves [][]byte) (logState, map[string][]byte, error) {
	// The range is built over a copy, so a failed append leaves st as it was.
	rng, err := factory.NewRange(0, st.size, append([][]byte(nil), st.hashes...))
	if err != nil {
		return logState{}, nil, &CorruptTableError{Key: string(logHeadKey(id)), Reason: err.Error()}
	}

	staged := make(map[string][]byte)
	var stageErr error
	visit := func(nid compact.NodeID, hash []byte) {
		if stageErr != nil {
			return
		}
		k := nodeKey(id, nid.Level, nid.Index)
		if err := w.Set(k, hash); err != nil {
			stageErr = &TableWriteError{Err: err}
			return
		}
		staged[string(k)] = hash
	}
	for _, e := range leaves {
		if err := rng.Append(hasher.HashLeaf(e), visit); err != nil {
			return logState{}, nil, &CorruptTableError{Key: string(logHeadKey(id)), Reason: err.Error()}
		}
		if stageErr != nil {
			return logState{}, nil, stageErr
		}
	}

	size := st.size + uint64(len(leaves))
	hashes := rng.Hashes()
	if err := w.Set(logHeadKey(id), encodeLogHead(size, st.lo, hashes)); err != nil {
		return logState{}, nil, &TableWriteError{Err: err}
	}
	st.size, st.hashes = size, hashes
	return st, staged, nil
}

// Checkpoint stages a signed checkpoint for id's tree at size, after
// checking that size is no larger than the log's and that root is the tree's
// root at size. The root is computed from the stored perfect subtrees
// covering [0, size), read from r through the nodes the current Update call
// has staged, so the answer does not depend on whether r sees the batch.
// The signature is not verified here: the proposing service and every client
// verify it, and apply stays independent of a verifier's version. A
// checkpoint is a record beside the tree, never a leaf of it, and a size has
// at most one.
func (c *Chain) Checkpoint(r storage.Reader, w Writer, id LogID, size uint64, root, signed []byte) error {
	if err := id.Validate(); err != nil {
		return err
	}
	if len(signed) == 0 {
		return &EmptyCheckpointError{Size: size}
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	st, ok := c.logs[id]
	if !ok {
		return &LogNotFoundError{ID: id}
	}
	if size > st.size {
		return &SizeOutOfRangeError{Size: size, Limit: st.size}
	}
	get := c.stagedGet(r)
	ck := checkpointKey(id, size)
	existing, err := get(ck)
	if err != nil {
		return &TableReadError{Err: err}
	}
	if existing != nil {
		return &CheckpointExistsError{Size: size}
	}

	var want []byte
	if size == st.size {
		want, err = rangeRoot(id, st.size, st.hashes)
	} else {
		want, err = rootAt(get, id, size)
	}
	if err != nil {
		return err
	}
	if !bytes.Equal(want, root) {
		return &CheckpointRootMismatchError{Size: size}
	}

	if err := w.Set(ck, signed); err != nil {
		return &TableWriteError{Err: err}
	}
	c.staged[string(ck)] = signed
	return nil
}

// ExpireBodies deletes id's entry bodies in [lo, upper), where lo is the
// log's own low-watermark and upper is cutoff clamped to the log's size and
// to lo+MaxPrunePerCommand, then advances lo to upper. Leaf and node hashes
// stay, so the tree and every proof stay valid; an expired entry reads as
// expired. Both bounds come from the in-memory head alone, as Prune's do. It
// returns the number of bodies expired; the caller issues it again with the
// same cutoff to continue past the cap.
func (c *Chain) ExpireBodies(w Writer, id LogID, cutoff uint64) (expired int, err error) {
	if err := id.Validate(); err != nil {
		return 0, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.auditInstanceLocked(id) {
		// The audit instance's retention is audit Prune (OpAuditPrune),
		// which prunes its events through pkg/audit.
		return 0, &ReservedLogError{ID: id}
	}
	st, ok := c.logs[id]
	if !ok {
		return 0, &LogNotFoundError{ID: id}
	}
	if !st.allowExpire {
		return 0, &ExpireNotAllowedError{ID: id}
	}
	upper := min(cutoff, st.size, st.lo+MaxPrunePerCommand)
	if upper <= st.lo {
		return 0, nil
	}
	for index := st.lo; index < upper; index++ {
		if err := w.Delete(entryKey(id, index)); err != nil {
			return int(index - st.lo), &TableWriteError{Err: err}
		}
	}
	expired = int(upper - st.lo)
	if err := w.Set(logHeadKey(id), encodeLogHead(st.size, upper, st.hashes)); err != nil {
		return expired, &TableWriteError{Err: err}
	}
	st.lo = upper
	c.logs[id] = st
	return expired, nil
}

// AuditDatabase and AuditLog name a tenant's audit instance: the log AuditLog
// in the tenant's reserved system database (lifecycle.SystemDatabase).
const (
	AuditDatabase = "system"
	AuditLog      = "audit"
)

// AuditLogID returns the LogID of tenantID's audit instance.
func AuditLogID(tenantID string) LogID {
	return LogID{TenantID: tenantID, Database: AuditDatabase, Log: AuditLog}
}

// IsAuditLog reports whether id names a tenant's audit instance.
// auditInstanceLocked reports whether id names a tenant's audit instance
// that exists: a log under the audit name whose state is the instance's.
// The audit name is reserved from the named-log Create, Append and
// ExpireBodies only then, so the reservation depends on committed state,
// not on the binary: before the instance exists those commands apply as
// they did before it, and the instance is created only by an audit append,
// which is proposed only once every replica is upgraded. The caller holds
// c.mu.
func (c *Chain) auditInstanceLocked(id LogID) bool {
	st, ok := c.logs[id]
	return ok && st.audit
}

func IsAuditLog(id LogID) bool {
	return id.Database == AuditDatabase && id.Log == AuditLog
}

// AuditDescriptor returns the descriptor of tenantID's audit instance whose
// leaf 0 is the audit event at sequence treeStart: it allows ExpireBodies
// (Prune), and nothing signs its heads until a key is rotated in.
func AuditDescriptor(tenantID string, treeStart uint64) Descriptor {
	return Descriptor{
		Origin:            AuditLogID(tenantID).String(),
		Hash:              HashSHA256,
		AllowExpireBodies: true,
		TreeStart:         treeStart,
		Audit:             true,
	}
}

// AppendAudit appends one event to tenantID's linear chain exactly as Append
// does, and the record's bytes -- the marshaled event with its PrevHash -- as
// the next leaf of the tenant's audit instance. The instance's first append
// creates it, its descriptor recording the event's sequence as TreeStart
// (S₀), so leaf 0 commits through its PrevHash to the whole chain before it.
// The body is stored once, under the linear chain's key; the instance holds
// the tree.
func (c *Chain) AppendAudit(w Writer, tenantID string, encode Encoder) (seq uint64, err error) {
	if err := ValidateTenantID(tenantID); err != nil {
		return 0, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	return c.appendEvent(w, tenantID, encode, true)
}

// appendEvent appends one event to tenantID's linear chain and, when the
// tenant's audit instance exists or create is set, as the instance's next
// leaf, creating the instance with TreeStart at the event's sequence if it
// does not yet exist. Leaf i of the instance is therefore always the event at
// TreeStart+i, whichever opcode carried it. c.mu must be held.
func (c *Chain) appendEvent(w Writer, tenantID string, encode Encoder, create bool) (uint64, error) {
	id := AuditLogID(tenantID)
	st, exists := c.logs[id]
	if exists && !st.audit {
		// A log created under the audit name before the instance existed
		// is never reinterpreted and no instance is created over it: the
		// event only extends the linear chain, as it did before the
		// instance, so the tenant's trail keeps running.
		exists, create = false, false
	}
	head, body, err := c.appendLinear(w, tenantID, encode)
	if err != nil {
		return 0, err
	}
	if !exists && !create {
		c.heads[tenantID] = head
		return head.seq, nil
	}
	var descBody []byte
	if !exists {
		// A struct of strings, integers and a bool always marshals, and
		// always to the same bytes.
		descBody, _ = json.Marshal(AuditDescriptor(tenantID, head.seq))
		if err := w.Set(descriptorKey(id), descBody); err != nil {
			return 0, &TableWriteError{Err: err}
		}
		st = logState{allowExpire: true, treeStart: head.seq, audit: true}
	}
	next, staged, err := appendLeaves(w, id, st, [][]byte{body})
	if err != nil {
		return 0, err
	}
	if descBody != nil {
		c.staged[string(descriptorKey(id))] = descBody
	}
	maps.Copy(c.staged, staged)
	c.logs[id] = next
	c.heads[tenantID] = head
	return head.seq, nil
}
