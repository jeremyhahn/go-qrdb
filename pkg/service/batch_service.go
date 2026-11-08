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
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"strconv"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"gopkg.in/yaml.v3"

	"github.com/jeremyhahn/go-qrdb/pkg/codec"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// ---------------------------------------------------------------------------
// Import resource limits
//
// These bound every stage of parseAndImport so that no single import -- a
// zip/tar bomb, an archive with millions of entries, a dataset with
// millions of rows, an oversized key/value, or an oversized encoded Raft
// command -- can exhaust node memory or CPU. A chunked import session's own
// per-session byte/chunk quotas (config.ImportSessionConfig, enforced at
// ImportAppend time -- see maxSessionBytes et al.) bound the raw input
// before it ever reaches parseAndImport; these limits apply unconditionally
// on top of that, including to callers (like the embedded transport) that
// have no request-size cap of their own.
// ---------------------------------------------------------------------------

const (
	// maxImportExpandedBytes bounds the cumulative decompressed bytes
	// produced while extracting a zip/tar/tar.gz archive, summed across
	// every member of a single archive. It defends against archive ("zip
	// bomb") inputs that are small on the wire but expand to gigabytes in
	// memory.
	maxImportExpandedBytes = 400 << 20 // 400 MB

	// maxImportArchiveEntries bounds the number of entries examined within
	// a single zip/tar/tar.gz archive. Defends against archives containing
	// a huge number of tiny or empty entries, which can exhaust CPU
	// iterating per-entry bookkeeping even while individually staying
	// under maxImportExpandedBytes.
	maxImportArchiveEntries = 10_000

	// maxImportRows bounds the number of records a single import will
	// process, regardless of source format. This bounds both the in-memory
	// parsed-record slice and the number of encoded Raft operations built
	// from it. Callers with larger datasets should split them across
	// multiple chunked import sessions.
	maxImportRows = 1_000_000

	// maxImportKeySize and maxImportValueSize bound the size of any single
	// imported key/value.
	//
	// maxImportKeySize is protocol.MaxClientKeyLength, not a separate
	// literal: it is the same global key-length policy every write path
	// enforces (go-qrdb#592), and a second definition of one bound is
	// exactly the drift that family of defects comes from. It is no longer a
	// wire-format ceiling here either way — protocol.EncodeOp frames lengths
	// in protocol.FrameSize-byte fields, wide enough that truncation is not
	// a concern on its own — but it must still stay comfortably below
	// maxImportProposalBytes so that a single operation always fits in one
	// Raft log entry, the invariant buildImportProposalChunks relies on to
	// guarantee every chunk contains at least one op. The value bound is
	// sized for real payloads such as BLOB columns, leaving ample headroom
	// under the 8 MiB proposal ceiling for the key, the op framing, and the
	// batch header.
	maxImportKeySize   = protocol.MaxClientKeyLength
	maxImportValueSize = 4 << 20 // 4 MB

	// maxImportProposalBytes bounds the size of a single encoded batch
	// command sent to Raft via SyncPropose. It matches go-quicraft's
	// proto.MaxEntrySize (8MB), the hard ceiling the consensus layer
	// enforces on any single log entry. Non-transactional imports whose
	// encoded operations exceed this are split across multiple sequential
	// proposals (each its own MVCC revision); transactional imports --
	// which require every row to land in a single atomic revision -- are
	// rejected outright rather than silently losing their all-or-nothing
	// guarantee by chunking.
	maxImportProposalBytes = 8 << 20 // 8 MB

	// importBatchHeaderOverhead is protocol.EncodeBatch's fixed per-command
	// overhead (1-byte op code + one framed op count), used when estimating
	// the encoded size of a chunk during proposal chunking. It is derived
	// from protocol.FrameSize so the estimate cannot drift from the encoder.
	importBatchHeaderOverhead = 1 + protocol.FrameSize

	// defaultImportSessionMaxBytes, defaultImportSessionMaxChunks,
	// defaultImportSessionsPerTenant and defaultImportSessionTTLSeconds
	// mirror pkg/config's DefaultImportSessionMaxBytes/MaxChunks/
	// PerTenant/TTLSeconds exactly. pkg/service does not import pkg/config
	// (no layer below the server orchestrator reads YAML), so the
	// production values are duplicated here rather than shared; the two
	// are proven to agree by TestImportSessionDefaultsMatchConfigPackage
	// (pkg/config, which may import pkg/service -- the reverse would not
	// be allowed -- without creating a cycle).
	defaultImportSessionMaxBytes   = 64 << 20
	defaultImportSessionMaxChunks  = 16384
	defaultImportSessionsPerTenant = 16
	defaultImportSessionTTLSeconds = 3600
)

// entityOpTypeHandlers maps entity operation type strings to the
// corresponding protocol op code for O(1) dispatch.
var entityOpTypeHandlers = map[string]byte{
	"put":    protocol.OpMVCCPut,
	"delete": protocol.OpMVCCDelete,
}

// BatchResponse is the result of an atomic batch.
type BatchResponse struct {
	// Revision is the single MVCC revision every write in the batch
	// committed at. It is zero when the batch held no put or delete, since
	// nothing was committed.
	Revision int64

	// Responses holds the per-operation results in the order of the
	// request's operations.
	Responses []OpResponse
}

// BatchServicer provides atomic multi-operation execution at the KV layer.
// All operations in a batch are applied within a single MVCC revision,
// guaranteeing atomicity without the conditional logic of transactions.
type BatchServicer interface {
	// Execute applies the given operations atomically. All operations
	// succeed or fail together. Returns the per-operation results in
	// the same order as the input, and the single MVCC revision every
	// write in the batch committed at.
	Execute(ctx context.Context, ops []Op) (*BatchResponse, error)

	// ExecuteWithMode applies the given operations atomically with the
	// specified write durability. All operations succeed or fail
	// together. Returns the per-operation results in the same order
	// as the input.
	ExecuteWithMode(ctx context.Context, ops []Op, mode writemode.Mode) (*BatchResponse, error)

	// ExecuteEntity executes a batch of entity-typed operations with
	// consistency and write mode options. Entity keys are constructed
	// from the EntityType and ID fields. All operations are applied
	// atomically within a single MVCC revision.
	ExecuteEntity(ctx context.Context, req *BatchEntityRequest) (*BatchEntityResponse, error)

	// ImportBegin starts a chunked import session scoped to the caller's
	// (tenant, database) and (tenant, identity) -- both read from ctx, the
	// same way every other data-plane call resolves them. opts configures
	// the import; KeyPrefix is set by the per-database wrapper, never the
	// caller. Returns the session ID the caller passes to ImportAppend,
	// ImportCommit, and ImportAbort.
	ImportBegin(ctx context.Context, opts ImportOptions) (sessionID string, err error)

	// ImportAppend stages chunk as sequence seq of sessionID and renews
	// the session's lease. seq must be exactly one more than the last
	// appended sequence (a new chunk), or equal to the last appended
	// sequence with byte-identical content (an idempotent replay of a call
	// whose response the caller may not have seen); any other value is
	// refused with *ImportSequenceError. The caller's (tenant, identity)
	// must match what ImportBegin recorded, checked explicitly -- refused
	// with *ImportSessionOwnerError on a mismatch in either dimension --
	// never inferred from the session ID being hard to guess.
	ImportAppend(ctx context.Context, sessionID string, seq uint64, chunk []byte) error

	// ImportCommit applies every staged chunk of sessionID atomically --
	// the concatenation of every chunk, in sequence order, is parsed and
	// imported as a single file -- and releases the session on success.
	// A failure partway through commit
	// applies nothing and leaves the session staged for inspection or
	// retry. The caller's (tenant, identity) must match the session's.
	ImportCommit(ctx context.Context, sessionID string) (*ImportResult, error)

	// ImportAbort releases sessionID -- its lease, and therefore its
	// metadata record and every staged chunk -- without applying
	// anything. The caller's (tenant, identity) must match the session's.
	ImportAbort(ctx context.Context, sessionID string) error
}

// ImportOptions configures batch import behavior.
type ImportOptions struct {
	// Format specifies the file format: "csv", "json", "yaml", "ndjson",
	// "archive", or "auto". When "auto" or empty, the format is inferred
	// from Filename and the first 8 KB of data.
	Format string

	// Filename is used for format auto-detection when Format is "auto" or
	// empty. It has no effect when Format is explicitly set to a known value.
	Filename string

	// KeyField is the field name to extract as the key.
	KeyField string

	// ValueMode specifies how to extract the value: "field" uses a
	// specific field, "row" serializes the entire row as JSON.
	ValueMode string

	// ValueField is the field name for the value when ValueMode is "field".
	ValueField string

	// SkipHeader indicates whether to skip the first row in CSV files.
	// Defaults to true.
	SkipHeader bool

	// Transactional enables all-or-nothing mode where any error causes
	// the entire import to be rolled back.
	Transactional bool

	// KeyPrefix is prepended to every imported key before it is written.
	// It is set by the namespaced (per-database) batch wrapper to the
	// composite "<tenant>/<db>/" namespace so imported rows land in the
	// same keyspace as every other write to that database. Empty means
	// no prefixing (the unscoped/system path). Error rows report the
	// original user-supplied key, not the prefixed form.
	KeyPrefix string
}

// ImportResult contains statistics from a batch import operation.
type ImportResult struct {
	// Total is the total number of rows processed.
	Total int

	// Succeeded is the number of rows successfully imported.
	Succeeded int

	// Failed is the number of rows that failed to import.
	Failed int

	// Errors contains details about failed rows.
	Errors []ImportError
}

// ImportError describes a single row that failed during import.
type ImportError struct {
	// Row is the 1-based row number that failed.
	Row int `json:"row"`

	// Key is the extracted key for the failed row.
	Key string `json:"key"`

	// Message describes why the row failed.
	Message string `json:"message"`
}

// BatchService implements BatchServicer backed by a QuicRaft host running
// an MVCCStateMachine.
type BatchService struct {
	host    Host
	shardID uint64

	// expandedBudget is the cumulative decompressed-bytes budget a single
	// archive import may consume. Zero means maxImportExpandedBytes, so the
	// zero value of BatchService carries the production limit and a caller
	// that builds the struct directly is not silently unprotected.
	//
	// It is a field rather than a constant read at the point of use so that
	// tests can prove the refusal without allocating the production budget.
	// The decision under test -- expansionLimitedReader refusing once the
	// budget is exhausted -- is identical at any budget, but asserting it at
	// 400 MB means materialising 400 MB through io.ReadAll under -race, which
	// measured a 6.9 GB peak RSS and OOM-killed pkg/service on CI three times
	// in one day (go-qrdb#148).
	expandedBudget int64

	// importKeyGuard, when set, is consulted by importRecords for every
	// row's storedKey before it is encoded and proposed, refusing the row
	// (per Import's existing row-failure semantics: a transactional import
	// aborts entirely, a non-transactional one records the row as failed
	// and continues) when the guard returns an error. It exists because
	// Import proposes its encoded ops directly and never calls Execute,
	// so a caller-named key reaching into a reserved namespace -- the
	// cluster CA's key material, specifically -- has no other
	// interception point. nil means no guard: the zero value of
	// BatchService imports every well-formed row, as it always has. Set
	// via SetImportKeyGuard, which NewHardConfinedBatch calls through the
	// ImportKeyGuardSetter interface.
	importKeyGuard func(key string) error

	// Import session quotas (see pkg/config.ImportSessionConfig, which
	// pkg/server resolves into WithImportSessionLimits at construction).
	// Zero or negative resolves to the package default for each, via the
	// importMaxSessionBytes/importMaxChunksPerSession/
	// importMaxSessionsPerTenant/importSessionTTLSeconds accessors, so the
	// zero value of BatchService carries production bounds exactly as
	// expandedBudget does.
	maxSessionBytes      int64
	maxChunksPerSession  int
	maxSessionsPerTenant int
	importSessionTTLSecs int64

	// leaseSvc backs an import session's lease (grant at ImportBegin,
	// renew at ImportAppend, revoke at ImportCommit/ImportAbort). nil
	// resolves to a LeaseService over the same host and shard, via
	// leaseService(); a field rather than always constructing one inline
	// so a test can inject a fake that forces a lease failure.
	leaseSvc LeaseServicer
}

// Compile-time interface assertion.
var _ ImportKeyGuardSetter = (*BatchService)(nil)

// SetImportKeyGuard installs (or clears, with nil) the per-row key guard
// importRecords consults before proposing a write. See ImportKeyGuardSetter;
// the only caller in this codebase is NewHardConfinedBatch.
func (s *BatchService) SetImportKeyGuard(guard func(key string) error) {
	s.importKeyGuard = guard
}

// BatchServiceOption configures optional BatchService behavior at
// construction.
type BatchServiceOption func(*BatchService)

// WithImportSessionLimits configures the chunked import session quotas:
// maxBytes and maxChunks bound a single session, maxSessionsPerTenant
// bounds a tenant's concurrent sessions, and ttlSeconds is the session
// lease's TTL. Any non-positive argument leaves that quota at its package
// default.
func WithImportSessionLimits(maxBytes int64, maxChunks, maxSessionsPerTenant int, ttlSeconds int64) BatchServiceOption {
	return func(s *BatchService) {
		s.maxSessionBytes = maxBytes
		s.maxChunksPerSession = maxChunks
		s.maxSessionsPerTenant = maxSessionsPerTenant
		s.importSessionTTLSecs = ttlSeconds
	}
}

// WithLeaseServicer overrides the LeaseServicer an import session's lease is
// granted, renewed, and revoked through. Exists for tests that need to force
// a lease failure; production callers never need it, since the zero value
// resolves to a LeaseService over the same host and shard.
func WithLeaseServicer(svc LeaseServicer) BatchServiceOption {
	return func(s *BatchService) { s.leaseSvc = svc }
}

// NewBatchService creates a new BatchServicer that operates on the specified
// shard. The shard must be running an MVCCStateMachine for batch operations
// to succeed.
func NewBatchService(host Host, shardID uint64, opts ...BatchServiceOption) BatchServicer {
	s := &BatchService{
		host:    host,
		shardID: shardID,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// importExpandedBudget reports the archive expansion budget in force for this
// service, resolving a zero or negative value to the production limit so the
// bound can never be accidentally disabled.
func (s *BatchService) importExpandedBudget() int64 {
	if s.expandedBudget <= 0 {
		return maxImportExpandedBytes
	}
	return s.expandedBudget
}

// leaseService returns the LeaseServicer an import session's lease is
// granted, renewed, and revoked through.
func (s *BatchService) leaseService() LeaseServicer {
	if s.leaseSvc != nil {
		return s.leaseSvc
	}
	return NewLeaseService(s.host, s.shardID)
}

// importMaxSessionBytes resolves the configured per-session byte budget, or
// defaultImportSessionMaxBytes when unset (non-positive).
func (s *BatchService) importMaxSessionBytes() int64 {
	if s.maxSessionBytes <= 0 {
		return defaultImportSessionMaxBytes
	}
	return s.maxSessionBytes
}

// importMaxChunksPerSession resolves the configured per-session chunk-count
// ceiling, or defaultImportSessionMaxChunks when unset (non-positive).
func (s *BatchService) importMaxChunksPerSession() int {
	if s.maxChunksPerSession <= 0 {
		return defaultImportSessionMaxChunks
	}
	return s.maxChunksPerSession
}

// importMaxSessionsPerTenant resolves the configured per-tenant concurrent-
// session ceiling, or defaultImportSessionsPerTenant when unset
// (non-positive).
func (s *BatchService) importMaxSessionsPerTenant() int {
	if s.maxSessionsPerTenant <= 0 {
		return defaultImportSessionsPerTenant
	}
	return s.maxSessionsPerTenant
}

// importSessionTTLSeconds resolves the configured session lease TTL, or
// defaultImportSessionTTLSeconds when unset (non-positive).
func (s *BatchService) importSessionTTLSeconds() int64 {
	if s.importSessionTTLSecs <= 0 {
		return defaultImportSessionTTLSeconds
	}
	return s.importSessionTTLSecs
}

// Execute applies the given operations atomically through Raft consensus.
// Write operations (Put, Delete) are executed within a single MVCC revision.
// Read operations (Get, Range) are performed via linearizable reads before
// writes are committed, providing a consistent snapshot of the pre-batch
// state. Returns the per-operation results in the same order as the input.
func (s *BatchService) Execute(ctx context.Context, ops []Op) (*BatchResponse, error) {
	return s.execute(ctx, ops, func(cmd []byte) (sm.Result, error) {
		return propose(ctx, s.host, s.shardID, cmd)
	})
}

// execute is the single body behind Execute and ExecuteWithMode; proposeCmd
// replicates the encoded write batch with the caller's durability.
func (s *BatchService) execute(ctx context.Context, ops []Op, proposeCmd func(cmd []byte) (sm.Result, error)) (*BatchResponse, error) {
	responses := make([]OpResponse, len(ops))

	// Execute read operations first via SyncRead. Reads observe the
	// pre-batch state, consistent with etcd-style batch semantics where
	// reads within a batch see the snapshot before the batch's mutations.
	for i, op := range ops {
		switch op.Type {
		case OpGet:
			resp, err := s.executeGet(ctx, op)
			if err != nil {
				return nil, err
			}
			responses[i] = resp

		case OpRange:
			resp, err := s.executeRange(ctx, op)
			if err != nil {
				return nil, err
			}
			responses[i] = resp

		case OpPut, OpDelete:

		default:
			return nil, &UnsupportedTransactionOpError{OpType: op.Type}
		}
	}

	// Collect and execute write operations through Raft consensus.
	var writeOps [][]byte
	for _, op := range ops {
		if op.Type == OpPut || op.Type == OpDelete {
			encoded, err := encodeServiceOp(op)
			if err != nil {
				return nil, &EncodeRequestError{Err: err}
			}
			writeOps = append(writeOps, encoded)
		}
	}

	var revision int64
	if len(writeOps) > 0 {
		cmd, encErr := protocol.EncodeBatch(writeOps)
		if encErr != nil {
			return nil, &EncodeRequestError{Err: encErr}
		}
		result, err := proposeCmd(cmd)
		if err != nil {
			return nil, translateProposeError(err)
		}
		// Surface a state-machine-encoded application error (e.g. a unique-index
		// violation that rejected the batch without stalling consensus) instead
		// of falsely reporting the writes as succeeded.
		if resultErr := checkResultError(result); resultErr != nil {
			return nil, resultErr
		}
		// The MVCC state machine applies the whole batch at one new revision
		// and returns it as the result value.
		revision = int64(result.Value)
	}

	// Mark write operations as succeeded.
	for i, op := range ops {
		if op.Type == OpPut || op.Type == OpDelete {
			responses[i] = OpResponse{Succeeded: true}
		}
	}

	return &BatchResponse{Revision: revision, Responses: responses}, nil
}

// ExecuteWithMode applies the given operations atomically with the specified
// write durability through Raft consensus. Read operations (Get, Range) are
// performed via linearizable reads before writes are committed. Write
// operations (Put, Delete) are proposed using ProposeWithMode. Returns the
// per-operation results in the same order as the input.
func (s *BatchService) ExecuteWithMode(ctx context.Context, ops []Op, mode writemode.Mode) (*BatchResponse, error) {
	return s.execute(ctx, ops, func(cmd []byte) (sm.Result, error) {
		return proposeWithMode(ctx, s.host, s.shardID, cmd, mode)
	})
}

// executeGet performs a linearizable read for a single key. Returns an
// OpResponse with the key-value pair, or an empty Kvs slice if the key
// does not exist.
func (s *BatchService) executeGet(ctx context.Context, op Op) (OpResponse, error) {
	// When RangeEnd is set, this is actually a range query encoded as a GET.
	if len(op.RangeEnd) > 0 {
		return s.executeRange(ctx, op)
	}

	result, err := s.host.SyncRead(ctx, s.shardID, MVCCGetQuery{Key: op.Key})
	if err != nil {
		// Key not found is not an error for batch GET -- return empty kvs.
		var knf keyNotFoundErr
		if errors.As(err, &knf) {
			return OpResponse{Succeeded: true}, nil
		}
		return OpResponse{}, s.translateReadError(err)
	}

	kv, ok := result.(*KeyValue)
	if !ok {
		return OpResponse{}, &ReadError{Err: &unexpectedResultTypeError{}}
	}

	return OpResponse{
		Kvs:       []*KeyValue{kv},
		Count:     1,
		Succeeded: true,
	}, nil
}

// executeRange performs a linearizable range read over [Key, RangeEnd).
// Returns an OpResponse with matching key-value pairs.
func (s *BatchService) executeRange(ctx context.Context, op Op) (OpResponse, error) {
	result, err := s.host.SyncRead(ctx, s.shardID, MVCCRangeQuery{
		Start: op.Key,
		End:   op.RangeEnd,
	})
	if err != nil {
		return OpResponse{}, s.translateReadError(err)
	}

	rr, ok := result.(*MVCCRangeResult)
	if !ok {
		return OpResponse{}, &ReadError{Err: &unexpectedResultTypeError{}}
	}

	return OpResponse{
		Kvs:       rr.KeyValues,
		Count:     int64(len(rr.KeyValues)),
		Succeeded: true,
	}, nil
}

// translateReadError converts QuicRaft read errors to service-level typed
// errors.
func (s *BatchService) translateReadError(err error) error {
	var nle *quicraft.NotLeaderError
	if errors.As(err, &nle) {
		return &NotLeaderError{
			ShardID:       nle.ShardID,
			LeaderID:      nle.LeaderID,
			LeaderAddress: nle.LeaderAddress,
		}
	}
	return &ReadError{Err: err}
}

// ExecuteEntity executes a batch of entity-typed operations atomically
// through Raft consensus. Each operation's entity key is constructed as
// "{entityType}/{id}" and encoded using the standard batch protocol.
func (s *BatchService) ExecuteEntity(ctx context.Context, req *BatchEntityRequest) (*BatchEntityResponse, error) {
	if req == nil || len(req.Ops) == 0 {
		return nil, &BatchEntityEmptyOpsError{}
	}

	// Validate and encode each entity operation.
	encodedOps := make([][]byte, len(req.Ops))
	for i, op := range req.Ops {
		if op.EntityType == "" {
			return nil, &EntityEmptyTypeError{}
		}
		if op.ID == "" {
			return nil, &EntityEmptyIDError{EntityType: op.EntityType}
		}

		protocolOp, ok := entityOpTypeHandlers[op.Type]
		if !ok {
			return nil, &InvalidEntityOpTypeError{OpType: op.Type}
		}

		key := []byte(entityKey(op.EntityType, op.ID))
		encoded, encErr := protocol.EncodeOp(protocolOp, key, op.Data, nil, 0)
		if encErr != nil {
			return nil, &EncodeRequestError{Err: encErr}
		}
		encodedOps[i] = encoded
	}

	cmd, encErr := protocol.EncodeBatch(encodedOps)
	if encErr != nil {
		return nil, &EncodeRequestError{Err: encErr}
	}

	shardID := s.shardID
	if req.ShardID != 0 {
		shardID = req.ShardID
	}

	result, err := propose(ctx, s.host, shardID, cmd)
	if err != nil {
		return nil, translateProposeError(err)
	}
	// Surface a state-machine-encoded application error (e.g. a unique-index
	// violation that rejected the batch without stalling consensus) instead
	// of falsely reporting the writes as succeeded.
	if resultErr := checkResultError(result); resultErr != nil {
		return nil, resultErr
	}

	// All ops succeed atomically; build per-operation results.
	results := make([]BatchEntityOpResult, len(req.Ops))
	for i := range req.Ops {
		results[i] = BatchEntityOpResult{Index: i}
	}

	return &BatchEntityResponse{Results: results}, nil
}

// ---------------------------------------------------------------------------
// Import implementation
// ---------------------------------------------------------------------------

// parseAndImport resolves data's format (detecting it from opts.Filename and
// a content sample when opts.Format is "auto" or empty), parses it into
// records, and imports them. Supports CSV, JSON (single object, array, or
// NDJSON), and archive formats (zip, tar, tar.gz). It is ImportCommit's
// tail: data is already the concatenation of a session's staged chunks,
// each already bounded by the per-session byte quota at ImportAppend time,
// so no additional size check is needed here.
func (s *BatchService) parseAndImport(ctx context.Context, data []byte, opts ImportOptions) (*ImportResult, error) {
	if err := validateImportOptions(opts); err != nil {
		return nil, err
	}

	// Resolve "auto" (or empty) format by probing the filename and data.
	resolvedFormat := opts.Format
	if resolvedFormat == "auto" || resolvedFormat == "" {
		// Use first 8 KB as the detection sample to avoid buffering large files.
		sampleLen := len(data)
		if sampleLen > 8192 {
			sampleLen = 8192
		}
		detected, detectErr := codec.DetectFormat(opts.Filename, data[:sampleLen])
		if detectErr != nil {
			return nil, &ImportParseError{Err: detectErr}
		}
		resolvedFormat = detected
	}

	var records []map[string]string
	var err error

	switch resolvedFormat {
	case "csv":
		records, err = parseCSV(data, opts.SkipHeader)
	case "json":
		records, err = parseJSON(data)
	case "ndjson":
		records, err = parseNDJSON(data)
	case "yaml":
		records, err = parseYAML(data)
	case "archive", "zip", "tar", "tar.gz":
		records, err = parseArchive(data, opts.SkipHeader, s.importExpandedBudget())
	default:
		return nil, &ImportInvalidFormatError{Format: resolvedFormat}
	}

	if err != nil {
		return nil, err
	}

	if len(records) > maxImportRows {
		return nil, &ImportTooManyRowsError{Max: maxImportRows}
	}

	return s.importRecords(ctx, records, opts)
}

// validateImportOptions checks that all required options are present and valid.
func validateImportOptions(opts ImportOptions) error {
	switch opts.Format {
	case "csv", "json", "yaml", "ndjson", "archive", "auto", "":
	default:
		return &ImportInvalidFormatError{Format: opts.Format}
	}

	if opts.KeyField == "" {
		return &ImportMissingKeyFieldError{}
	}

	switch opts.ValueMode {
	case "field":
		if opts.ValueField == "" {
			return &ImportMissingValueFieldError{}
		}
	case "row":
	default:
		return &ImportInvalidValueModeError{Mode: opts.ValueMode}
	}

	return nil
}

// parseCSV parses CSV data into a slice of maps keyed by column headers.
func parseCSV(data []byte, skipHeader bool) ([]map[string]string, error) {
	reader := csv.NewReader(bytes.NewReader(data))

	// Read all records.
	allRows, err := reader.ReadAll()
	if err != nil {
		return nil, &ImportParseError{Err: err}
	}

	if len(allRows) == 0 {
		return nil, nil
	}

	// First row is always headers.
	headers := allRows[0]

	// Determine data rows.
	dataRows := allRows[1:]
	if !skipHeader {
		// If not skipping header, treat first row as data too.
		dataRows = allRows
	}

	records := make([]map[string]string, 0, len(dataRows))
	for _, row := range dataRows {
		record := make(map[string]string, len(headers))
		for i, h := range headers {
			if i < len(row) {
				record[h] = row[i]
			}
		}
		records = append(records, record)
	}

	return records, nil
}

// parseJSON parses JSON data into a slice of maps. Supports single objects,
// arrays of objects, and NDJSON (newline-delimited JSON).
func parseJSON(data []byte) ([]map[string]string, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, nil
	}

	// Try array first.
	if data[0] == '[' {
		var arr []map[string]interface{}
		if err := json.Unmarshal(data, &arr); err != nil {
			return nil, &ImportParseError{Err: err}
		}
		return convertJSONRecords(arr), nil
	}

	// Try single object.
	if data[0] == '{' && !bytes.Contains(data, []byte("\n{")) {
		var obj map[string]interface{}
		if err := json.Unmarshal(data, &obj); err != nil {
			return nil, &ImportParseError{Err: err}
		}
		return convertJSONRecords([]map[string]interface{}{obj}), nil
	}

	// NDJSON: newline-delimited JSON.
	var records []map[string]interface{}
	decoder := json.NewDecoder(bytes.NewReader(data))
	for {
		var obj map[string]interface{}
		err := decoder.Decode(&obj)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, &ImportParseError{Err: err}
		}
		records = append(records, obj)
	}

	return convertJSONRecords(records), nil
}

// parseNDJSON parses newline-delimited JSON (one JSON object per line).
// It is the explicit format handler for "ndjson" and "jsonl" files.
func parseNDJSON(data []byte) ([]map[string]string, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, nil
	}

	var records []map[string]interface{}
	decoder := json.NewDecoder(bytes.NewReader(data))
	for {
		var obj map[string]interface{}
		err := decoder.Decode(&obj)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, &ImportParseError{Err: err}
		}
		records = append(records, obj)
	}

	return convertJSONRecords(records), nil
}

// parseYAML parses YAML data into a slice of string maps. Supports a single
// mapping document or a sequence of mapping documents.
func parseYAML(data []byte) ([]map[string]string, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, nil
	}

	// Try sequence of maps first.
	var seq []map[string]interface{}
	if err := yaml.Unmarshal(data, &seq); err == nil && seq != nil {
		return convertJSONRecords(seq), nil
	}

	// Try single map.
	var obj map[string]interface{}
	if err := yaml.Unmarshal(data, &obj); err != nil {
		return nil, &ImportParseError{Err: err}
	}
	if obj == nil {
		return nil, nil
	}

	return convertJSONRecords([]map[string]interface{}{obj}), nil
}

// convertJSONRecords converts JSON objects with interface{} values to string maps.
func convertJSONRecords(objs []map[string]interface{}) []map[string]string {
	records := make([]map[string]string, len(objs))
	for i, obj := range objs {
		record := make(map[string]string, len(obj))
		for k, v := range obj {
			switch val := v.(type) {
			case string:
				record[k] = val
			case nil:
				record[k] = ""
			default:
				// Serialize non-string values as JSON.
				b, _ := json.Marshal(val)
				record[k] = string(b)
			}
		}
		records[i] = record
	}
	return records
}

// importExpansionLimiter bounds the total decompressed bytes and the total
// entry count examined while extracting a single zip/tar/tar.gz archive.
// The byte budget is shared across every member of the archive so the
// aggregate expansion is bounded, not just each member individually --
// this is what defends against zip/tar bombs (small on the wire, huge once
// decompressed).
type importExpansionLimiter struct {
	remainingBytes int64
	entries        int

	// budget is the value remainingBytes started from. Kept so a refusal can
	// report the limit actually applied rather than the compiled-in constant:
	// under a test budget the two differ, and an error naming the wrong one
	// would be a false statement about why the import was refused.
	budget int64
}

// newImportExpansionLimiter returns a limiter with a fresh budget for a single
// archive-parse attempt. A non-positive budget resolves to
// maxImportExpandedBytes so no caller can accidentally disable the bound.
func newImportExpansionLimiter(budget int64) *importExpansionLimiter {
	if budget <= 0 {
		budget = maxImportExpandedBytes
	}
	return &importExpansionLimiter{remainingBytes: budget, budget: budget}
}

// checkEntry increments the entry counter and returns
// ImportTooManyEntriesError once the archive-wide entry limit is exceeded.
// Called once per archive entry (file or directory) before it is examined,
// bounding total loop iterations regardless of entry size.
func (l *importExpansionLimiter) checkEntry() error {
	l.entries++
	if l.entries > maxImportArchiveEntries {
		return &ImportTooManyEntriesError{Max: maxImportArchiveEntries}
	}
	return nil
}

// limitReader wraps r so reads beyond the shared cumulative
// maxImportExpandedBytes budget fail with ImportArchiveExpandedTooLargeError
// instead of allocating unbounded memory.
func (l *importExpansionLimiter) limitReader(r io.Reader) io.Reader {
	return &expansionLimitedReader{r: r, limiter: l}
}

// expansionLimitedReader is an io.Reader that decrements a shared
// cumulative budget on every read, failing once the budget is exhausted.
type expansionLimitedReader struct {
	r       io.Reader
	limiter *importExpansionLimiter
}

// Read implements io.Reader.
func (er *expansionLimitedReader) Read(p []byte) (int, error) {
	if er.limiter.remainingBytes <= 0 {
		return 0, &ImportArchiveExpandedTooLargeError{Max: int(er.limiter.budget)}
	}
	if int64(len(p)) > er.limiter.remainingBytes {
		p = p[:er.limiter.remainingBytes]
	}
	n, err := er.r.Read(p)
	er.limiter.remainingBytes -= int64(n)
	return n, err
}

// isImportLimitError reports whether err is one of the resource-limit
// errors an archive extractor can produce (too many entries, or too much
// expanded content). parseArchive uses this to distinguish "this input is
// not a valid archive of this type, try the next format" from "this IS a
// valid archive of this type but exceeds a resource limit" -- the latter
// must propagate immediately rather than be masked by retrying the next
// format against the same (already identified) bytes.
func isImportLimitError(err error) bool {
	var entries *ImportTooManyEntriesError
	var expanded *ImportArchiveExpandedTooLargeError
	return errors.As(err, &entries) || errors.As(err, &expanded)
}

// parseArchive extracts and parses files from zip, tar, or tar.gz archives.
// Combines all CSV/JSON files found in the archive.
func parseArchive(data []byte, skipHeader bool, budget int64) ([]map[string]string, error) {
	// Try zip first.
	if records, err := parseZip(data, skipHeader, budget); err == nil {
		return records, nil
	} else if isImportLimitError(err) {
		return nil, err
	}

	// Try tar.gz.
	if records, err := parseTarGz(data, skipHeader, budget); err == nil {
		return records, nil
	} else if isImportLimitError(err) {
		return nil, err
	}

	// Try plain tar.
	if records, err := parseTar(bytes.NewReader(data), skipHeader, budget); err == nil {
		return records, nil
	} else if isImportLimitError(err) {
		return nil, err
	}

	return nil, &ImportArchiveError{Err: errors.New("unsupported archive format")}
}

// parseZip extracts and parses files from a zip archive. Decompressed
// member bytes are bounded by a shared importExpansionLimiter so a
// malicious zip cannot expand into an unbounded amount of memory.
func parseZip(data []byte, skipHeader bool, budget int64) ([]map[string]string, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}

	limiter := newImportExpansionLimiter(budget)

	var allRecords []map[string]string
	for _, f := range reader.File {
		if err := limiter.checkEntry(); err != nil {
			return nil, err
		}

		if f.FileInfo().IsDir() {
			continue
		}

		rc, err := f.Open()
		if err != nil {
			return nil, &ImportArchiveError{Err: err}
		}

		fileData, err := io.ReadAll(limiter.limitReader(rc))
		_ = rc.Close()
		if err != nil {
			if isImportLimitError(err) {
				return nil, err
			}
			return nil, &ImportArchiveError{Err: err}
		}

		records, err := parseArchiveFile(f.Name, fileData, skipHeader)
		if err != nil {
			return nil, err
		}
		allRecords = append(allRecords, records...)
	}

	return allRecords, nil
}

// parseTarGz extracts and parses files from a gzipped tar archive.
func parseTarGz(data []byte, skipHeader bool, budget int64) ([]map[string]string, error) {
	gzReader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer func() { _ = gzReader.Close() }()

	return parseTar(gzReader, skipHeader, budget)
}

// parseTar extracts and parses files from a tar archive. Member bytes are
// bounded by a shared importExpansionLimiter so a malicious tar (or
// gzipped tar) cannot expand into an unbounded amount of memory.
func parseTar(r io.Reader, skipHeader bool, budget int64) ([]map[string]string, error) {
	tarReader := tar.NewReader(r)
	limiter := newImportExpansionLimiter(budget)

	var allRecords []map[string]string
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}

		if err := limiter.checkEntry(); err != nil {
			return nil, err
		}

		if header.Typeflag != tar.TypeReg {
			continue
		}

		fileData, err := io.ReadAll(limiter.limitReader(tarReader))
		if err != nil {
			if isImportLimitError(err) {
				return nil, err
			}
			return nil, &ImportArchiveError{Err: err}
		}

		records, err := parseArchiveFile(header.Name, fileData, skipHeader)
		if err != nil {
			return nil, err
		}
		allRecords = append(allRecords, records...)
	}

	return allRecords, nil
}

// parseArchiveFile parses a single file from an archive based on its extension.
func parseArchiveFile(name string, data []byte, skipHeader bool) ([]map[string]string, error) {
	// Determine format from file extension.
	switch {
	case hasCSVExtension(name):
		return parseCSV(data, skipHeader)
	case hasJSONExtension(name):
		return parseJSON(data)
	default:
		// Skip unknown file types.
		return nil, nil
	}
}

// hasCSVExtension returns true if the filename ends with .csv.
func hasCSVExtension(name string) bool {
	if len(name) < 4 {
		return false
	}
	ext := name[len(name)-4:]
	return ext == ".csv" || ext == ".CSV"
}

// hasJSONExtension returns true if the filename ends with .json.
func hasJSONExtension(name string) bool {
	if len(name) < 5 {
		return false
	}
	ext := name[len(name)-5:]
	return ext == ".json" || ext == ".JSON"
}

// importRecords processes parsed records and imports them as key-value pairs.
func (s *BatchService) importRecords(ctx context.Context, records []map[string]string, opts ImportOptions) (*ImportResult, error) {
	if len(records) == 0 {
		return &ImportResult{Total: 0, Succeeded: 0, Failed: 0}, nil
	}

	result := &ImportResult{
		Total:  len(records),
		Errors: make([]ImportError, 0),
	}

	// Build operations from records.
	var encodedOps [][]byte
	var keys []string
	var rowNums []int

	for i, record := range records {
		rowNum := i + 1 // 1-based row number.

		key, ok := record[opts.KeyField]
		if !ok || key == "" {
			if opts.Transactional {
				return nil, &ImportTransactionAbortedError{
					Row:     rowNum,
					Key:     "",
					Message: "missing key field: " + opts.KeyField,
				}
			}
			result.Failed++
			result.Errors = append(result.Errors, ImportError{
				Row:     rowNum,
				Key:     "",
				Message: "missing key field: " + opts.KeyField,
			})
			continue
		}

		var value []byte
		var valueErr error

		switch opts.ValueMode {
		case "field":
			if v, exists := record[opts.ValueField]; exists {
				value = []byte(v)
			} else {
				valueErr = errors.New("missing value field: " + opts.ValueField)
			}
		case "row":
			value, valueErr = json.Marshal(record)
		}

		if valueErr != nil {
			if opts.Transactional {
				return nil, &ImportTransactionAbortedError{
					Row:     rowNum,
					Key:     key,
					Message: valueErr.Error(),
				}
			}
			result.Failed++
			result.Errors = append(result.Errors, ImportError{
				Row:     rowNum,
				Key:     key,
				Message: valueErr.Error(),
			})
			continue
		}

		// Prepend the namespace prefix (if any) to the stored key so
		// imported rows share the database's keyspace. The reported key
		// (in keys/errors) stays the original user-supplied value.
		storedKey := opts.KeyPrefix + key

		if s.importKeyGuard != nil {
			if guardErr := s.importKeyGuard(storedKey); guardErr != nil {
				if opts.Transactional {
					return nil, &ImportTransactionAbortedError{Row: rowNum, Key: key, Message: guardErr.Error()}
				}
				result.Failed++
				result.Errors = append(result.Errors, ImportError{Row: rowNum, Key: key, Message: guardErr.Error()})
				continue
			}
		}

		if len(storedKey) > maxImportKeySize {
			msg := "key exceeds maximum size of " + strconv.Itoa(maxImportKeySize) +
				" bytes (got " + strconv.Itoa(len(storedKey)) + ")"
			if opts.Transactional {
				return nil, &ImportTransactionAbortedError{Row: rowNum, Key: key, Message: msg}
			}
			result.Failed++
			result.Errors = append(result.Errors, ImportError{Row: rowNum, Key: key, Message: msg})
			continue
		}
		if len(value) > maxImportValueSize {
			msg := "value exceeds maximum size of " + strconv.Itoa(maxImportValueSize) +
				" bytes (got " + strconv.Itoa(len(value)) + ")"
			if opts.Transactional {
				return nil, &ImportTransactionAbortedError{Row: rowNum, Key: key, Message: msg}
			}
			result.Failed++
			result.Errors = append(result.Errors, ImportError{Row: rowNum, Key: key, Message: msg})
			continue
		}

		// Mirrors the two size checks above exactly: a row that cannot be
		// encoded aborts a transactional import and is counted as failed in a
		// non-transactional one. Recording the error without incrementing
		// Failed would under-report, and continuing past it in transactional
		// mode would break all-or-nothing.
		//
		// Unreachable today — the import key and value caps are below the
		// protocol's four-byte field bound — but the branch has to be right
		// for the same reason the caps are checked at all.
		encoded, encErr := protocol.EncodeOp(protocol.OpMVCCPut, []byte(storedKey), value, nil, 0)
		if encErr != nil {
			if opts.Transactional {
				return nil, &ImportTransactionAbortedError{Row: rowNum, Key: key, Message: encErr.Error()}
			}
			result.Failed++
			result.Errors = append(result.Errors, ImportError{Row: rowNum, Key: key, Message: encErr.Error()})
			continue
		}
		encodedOps = append(encodedOps, encoded)
		keys = append(keys, key)
		rowNums = append(rowNums, rowNum)
	}

	if len(encodedOps) == 0 {
		return result, nil
	}

	return s.proposeImportOps(ctx, result, encodedOps, keys, rowNums, opts.Transactional)
}

// importChunk is a contiguous group of encoded import operations (with
// their aligned original keys and row numbers) sized to fit within a
// single Raft proposal.
type importChunk struct {
	ops     [][]byte
	keys    []string
	rowNums []int
}

// buildImportProposalChunks partitions ops (with their aligned keys and row
// numbers) into groups whose encoded protocol.EncodeBatch payload does not
// exceed maxImportProposalBytes, the ceiling go-quicraft enforces on any
// single Raft log entry (proto.MaxEntrySize). Each chunk preserves its
// ops' original relative order. Since every op is already bounded by
// maxImportKeySize/maxImportValueSize (well under maxImportProposalBytes),
// every chunk is guaranteed to contain at least one op.
func buildImportProposalChunks(ops [][]byte, keys []string, rowNums []int) []importChunk {
	var chunks []importChunk
	var cur importChunk
	curBytes := importBatchHeaderOverhead

	for i, op := range ops {
		opBytes := protocol.FrameSize + len(op) // protocol.EncodeBatch's per-op framing: length prefix + payload.
		if len(cur.ops) > 0 && curBytes+opBytes > maxImportProposalBytes {
			chunks = append(chunks, cur)
			cur = importChunk{}
			curBytes = importBatchHeaderOverhead
		}
		cur.ops = append(cur.ops, op)
		cur.keys = append(cur.keys, keys[i])
		cur.rowNums = append(cur.rowNums, rowNums[i])
		curBytes += opBytes
	}
	if len(cur.ops) > 0 {
		chunks = append(chunks, cur)
	}
	return chunks
}

// proposeImportOps proposes the given encoded import operations through
// Raft, splitting them across multiple sequential proposals when their
// total encoded size exceeds maxImportProposalBytes. Transactional imports
// require every row to land in a single atomic MVCC revision, so an
// oversized transactional import is rejected outright (ImportProposalTooLargeError)
// instead of being silently chunked into multiple non-atomic revisions;
// non-transactional imports (which already only guarantee atomicity within
// pieces, not across the whole import) are chunked and applied piece by
// piece, with failures recorded per affected row rather than aborting the
// entire import.
func (s *BatchService) proposeImportOps(ctx context.Context, result *ImportResult, ops [][]byte, keys []string, rowNums []int, transactional bool) (*ImportResult, error) {
	totalBytes := importBatchHeaderOverhead
	for _, op := range ops {
		totalBytes += protocol.FrameSize + len(op)
	}

	if transactional && totalBytes > maxImportProposalBytes {
		return nil, &ImportProposalTooLargeError{Max: maxImportProposalBytes}
	}

	for _, chunk := range buildImportProposalChunks(ops, keys, rowNums) {
		cmd, encErr := protocol.EncodeBatch(chunk.ops)
		if encErr != nil {
			return nil, &EncodeRequestError{Err: encErr}
		}
		proposed, proposeErr := propose(ctx, s.host, s.shardID, cmd)
		if proposeErr != nil {
			err := translateProposeError(proposeErr)
			// An outcome-unknown fault (go-quicraft committed the entry and
			// then failed to deliver the response -- see
			// service.ErrOutcomeUnknown) is NEVER safe to record as "this
			// chunk's rows failed" and continue, in EITHER mode: the chunk
			// may actually have applied, so silently treating it as a clean
			// per-row failure could under-report Succeeded, and letting
			// ImportCommit's caller see an overall success for a batch of
			// unknown rows would be worse -- it revokes the session,
			// destroying the only copy of the unresolved chunk's bytes
			// (go-qrdb#576's security review). Abort the whole commit
			// instead: ImportCommit returns this error without revoking the
			// lease, so the session -- every staged chunk, byte for byte --
			// stays retryable exactly as it was before this call.
			if errors.Is(err, ErrOutcomeUnknown) {
				return nil, err
			}
			// A NotLeaderError proves the opposite: refused before the entry
			// could commit, so nothing was stored. Safe to treat exactly
			// like a clean result-level refusal below.
			if transactional {
				return nil, err
			}
			for i, rowNum := range chunk.rowNums {
				result.Errors = append(result.Errors, ImportError{
					Row:     rowNum,
					Key:     chunk.keys[i],
					Message: err.Error(),
				})
			}
			result.Failed += len(chunk.ops)
			continue
		}
		// A not-configured subsystem -- or a refused write guard, such as
		// the drop fence of a database being dropped -- arrives in the
		// RESULT, not as a Raft error, so a chunk that applied nothing
		// would otherwise be counted as imported (go-qrdb#62). Every error
		// checkResultError can return is marked ErrNotStored (the entry
		// committed and provably stored nothing), so it is always safe to
		// treat as a clean failure: returned in transactional mode, per-row
		// in the other.
		if err := checkResultError(proposed); err != nil {
			if transactional {
				return nil, err
			}
			for i, rowNum := range chunk.rowNums {
				result.Errors = append(result.Errors, ImportError{
					Row:     rowNum,
					Key:     chunk.keys[i],
					Message: err.Error(),
				})
			}
			result.Failed += len(chunk.ops)
			continue
		}
		result.Succeeded += len(chunk.ops)
	}

	return result, nil
}
