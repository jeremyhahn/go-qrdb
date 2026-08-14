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
	"gopkg.in/yaml.v3"

	"github.com/jeremyhahn/go-qrdb/pkg/codec"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// ---------------------------------------------------------------------------
// Import resource limits
//
// These bound every stage of Import so that no single call -- a huge raw
// file, a zip/tar bomb, an archive with millions of entries, a dataset with
// millions of rows, an oversized key/value, or an oversized encoded Raft
// command -- can exhaust node memory or CPU. Transports already cap the
// request bytes they accept (REST/gRPC/MCP: 100MB; QUIC: ~63MB), but these
// service-level limits apply unconditionally, including to callers (like
// the embedded transport) that have no request-size cap of their own.
// ---------------------------------------------------------------------------

const (
	// maxImportInputBytes bounds the raw bytes read from the Import file
	// argument. It matches the request-body cap already enforced by the
	// REST (http.MaxBytesReader) and gRPC (grpc.MaxRecvMsgSize) transports
	// so behavior is consistent regardless of which layer applies it.
	maxImportInputBytes = 100 << 20 // 100 MB

	// maxImportExpandedBytes bounds the cumulative decompressed bytes
	// produced while extracting a zip/tar/tar.gz archive, summed across
	// every member of a single archive. It defends against archive ("zip
	// bomb") inputs that are small on the wire but expand to gigabytes in
	// memory. Set to a small, fixed multiple of maxImportInputBytes rather
	// than left unbounded.
	maxImportExpandedBytes = 4 * maxImportInputBytes // 400 MB

	// maxImportArchiveEntries bounds the number of entries examined within
	// a single zip/tar/tar.gz archive. Defends against archives containing
	// a huge number of tiny or empty entries, which can exhaust CPU
	// iterating per-entry bookkeeping even while individually staying
	// under maxImportExpandedBytes.
	maxImportArchiveEntries = 10_000

	// maxImportRows bounds the number of records a single Import call will
	// process, regardless of source format. This bounds both the in-memory
	// parsed-record slice and the number of encoded Raft operations built
	// from it. Callers with larger datasets should split them across
	// multiple Import calls.
	maxImportRows = 1_000_000

	// maxImportKeySize and maxImportValueSize bound the size of any single
	// imported key/value.
	//
	// These are no longer wire-format ceilings: protocol.EncodeOp frames
	// lengths in protocol.FrameSize-byte fields, which is wide enough that
	// truncation is not a concern. They are deliberate operational bounds,
	// and they must stay comfortably below maxImportProposalBytes so that a
	// single operation always fits in one Raft log entry — the invariant
	// buildImportProposalChunks relies on to guarantee every chunk contains
	// at least one op.
	//
	// The key bound stays small: keys are identifiers, and 64 KiB is already
	// far beyond any legitimate use (MySQL, for comparison, caps an index key
	// at 3072 bytes). The value bound is sized for real payloads such as BLOB
	// columns, leaving ample headroom under the 8 MiB proposal ceiling for the
	// key, the op framing, and the batch header.
	maxImportKeySize   = 65535
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
)

// entityOpTypeHandlers maps entity operation type strings to the
// corresponding protocol op code for O(1) dispatch.
var entityOpTypeHandlers = map[string]byte{
	"put":    protocol.OpMVCCPut,
	"delete": protocol.OpMVCCDelete,
}

// BatchServicer provides atomic multi-operation execution at the KV layer.
// All operations in a batch are applied within a single MVCC revision,
// guaranteeing atomicity without the conditional logic of transactions.
type BatchServicer interface {
	// Execute applies the given operations atomically. All operations
	// succeed or fail together. Returns the per-operation results in
	// the same order as the input.
	Execute(ctx context.Context, ops []Op) ([]OpResponse, error)

	// ExecuteWithMode applies the given operations atomically with the
	// specified write durability. All operations succeed or fail
	// together. Returns the per-operation results in the same order
	// as the input.
	ExecuteWithMode(ctx context.Context, ops []Op, mode writemode.Mode) ([]OpResponse, error)

	// ExecuteEntity executes a batch of entity-typed operations with
	// consistency and write mode options. Entity keys are constructed
	// from the EntityType and ID fields. All operations are applied
	// atomically within a single MVCC revision.
	ExecuteEntity(ctx context.Context, req *BatchEntityRequest) (*BatchEntityResponse, error)

	// Import parses and imports key-value pairs from a file (CSV, JSON,
	// or archive). Returns statistics about the import operation including
	// succeeded and failed row counts.
	Import(ctx context.Context, file io.Reader, opts ImportOptions) (*ImportResult, error)
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
}

// NewBatchService creates a new BatchServicer that operates on the specified
// shard. The shard must be running an MVCCStateMachine for batch operations
// to succeed.
func NewBatchService(host Host, shardID uint64) BatchServicer {
	return &BatchService{
		host:    host,
		shardID: shardID,
	}
}

// Execute applies the given operations atomically through Raft consensus.
// Write operations (Put, Delete) are executed within a single MVCC revision.
// Read operations (Get, Range) are performed via linearizable reads before
// writes are committed, providing a consistent snapshot of the pre-batch
// state. Returns the per-operation results in the same order as the input.
func (s *BatchService) Execute(ctx context.Context, ops []Op) ([]OpResponse, error) {
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
		}
	}

	// Collect and execute write operations through Raft consensus.
	var writeOps [][]byte
	for _, op := range ops {
		if op.Type == OpPut || op.Type == OpDelete {
			encoded, err := encodeServiceOp(op)
			if err != nil {
				return nil, err
			}
			writeOps = append(writeOps, encoded)
		}
	}

	if len(writeOps) > 0 {
		cmd := protocol.EncodeBatch(writeOps)
		result, err := s.host.SyncPropose(ctx, s.shardID, cmd)
		if err != nil {
			return nil, s.translateProposeError(err)
		}
		// Surface a state-machine-encoded application error (e.g. a unique-index
		// violation that rejected the batch without stalling consensus) instead
		// of falsely reporting the writes as succeeded.
		if resultErr := checkResultError(result); resultErr != nil {
			return nil, resultErr
		}
	}

	// Mark write operations as succeeded.
	for i, op := range ops {
		if op.Type == OpPut || op.Type == OpDelete {
			responses[i] = OpResponse{Succeeded: true}
		}
	}

	return responses, nil
}

// ExecuteWithMode applies the given operations atomically with the specified
// write durability through Raft consensus. Read operations (Get, Range) are
// performed via linearizable reads before writes are committed. Write
// operations (Put, Delete) are proposed using ProposeWithMode. Returns the
// per-operation results in the same order as the input.
func (s *BatchService) ExecuteWithMode(ctx context.Context, ops []Op, mode writemode.Mode) ([]OpResponse, error) {
	responses := make([]OpResponse, len(ops))

	// Execute read operations first via SyncRead. Reads observe the
	// pre-batch state, consistent with etcd-style batch semantics.
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
		}
	}

	// Collect and execute write operations with the specified mode.
	var writeOps [][]byte
	for _, op := range ops {
		if op.Type == OpPut || op.Type == OpDelete {
			encoded, err := encodeServiceOp(op)
			if err != nil {
				return nil, err
			}
			writeOps = append(writeOps, encoded)
		}
	}

	if len(writeOps) > 0 {
		cmd := protocol.EncodeBatch(writeOps)
		result, err := s.host.ProposeWithMode(ctx, s.shardID, cmd, mode)
		if err != nil {
			return nil, s.translateProposeError(err)
		}
		if resultErr := checkResultError(result); resultErr != nil {
			return nil, resultErr
		}
	}

	// Mark write operations as succeeded.
	for i, op := range ops {
		if op.Type == OpPut || op.Type == OpDelete {
			responses[i] = OpResponse{Succeeded: true}
		}
	}

	return responses, nil
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
		encodedOps[i] = protocol.EncodeOp(protocolOp, key, op.Data, nil, 0)
	}

	cmd := protocol.EncodeBatch(encodedOps)

	shardID := s.shardID
	if req.ShardID != 0 {
		shardID = req.ShardID
	}

	result, err := s.host.SyncPropose(ctx, shardID, cmd)
	if err != nil {
		return nil, s.translateProposeError(err)
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

// translateProposeError converts QuicRaft proposal errors to service-level
// typed errors.
func (s *BatchService) translateProposeError(err error) error {
	var nle *quicraft.NotLeaderError
	if errors.As(err, &nle) {
		return &NotLeaderError{
			ShardID:       nle.ShardID,
			LeaderID:      nle.LeaderID,
			LeaderAddress: nle.LeaderAddress,
		}
	}
	return &ProposeError{Err: err}
}

// ---------------------------------------------------------------------------
// Import implementation
// ---------------------------------------------------------------------------

// Import parses and imports key-value pairs from a file. Supports CSV, JSON
// (single object, array, or NDJSON), and archive formats (zip, tar, tar.gz).
func (s *BatchService) Import(ctx context.Context, file io.Reader, opts ImportOptions) (*ImportResult, error) {
	if err := validateImportOptions(opts); err != nil {
		return nil, err
	}

	// Read file into memory for potential archive handling. Bounded to
	// maxImportInputBytes so the service is safe even when the calling
	// transport applies no request-size cap of its own.
	data, err := io.ReadAll(io.LimitReader(file, maxImportInputBytes+1))
	if err != nil {
		return nil, &ImportParseError{Err: err}
	}
	if len(data) > maxImportInputBytes {
		return nil, &ImportInputTooLargeError{Max: maxImportInputBytes}
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
		records, err = parseArchive(data, opts.SkipHeader)
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
}

// newImportExpansionLimiter returns a limiter with a fresh
// maxImportExpandedBytes budget for a single archive-parse attempt.
func newImportExpansionLimiter() *importExpansionLimiter {
	return &importExpansionLimiter{remainingBytes: maxImportExpandedBytes}
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
		return 0, &ImportArchiveExpandedTooLargeError{Max: maxImportExpandedBytes}
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
func parseArchive(data []byte, skipHeader bool) ([]map[string]string, error) {
	// Try zip first.
	if records, err := parseZip(data, skipHeader); err == nil {
		return records, nil
	} else if isImportLimitError(err) {
		return nil, err
	}

	// Try tar.gz.
	if records, err := parseTarGz(data, skipHeader); err == nil {
		return records, nil
	} else if isImportLimitError(err) {
		return nil, err
	}

	// Try plain tar.
	if records, err := parseTar(bytes.NewReader(data), skipHeader); err == nil {
		return records, nil
	} else if isImportLimitError(err) {
		return nil, err
	}

	return nil, &ImportArchiveError{Err: errors.New("unsupported archive format")}
}

// parseZip extracts and parses files from a zip archive. Decompressed
// member bytes are bounded by a shared importExpansionLimiter so a
// malicious zip cannot expand into an unbounded amount of memory.
func parseZip(data []byte, skipHeader bool) ([]map[string]string, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}

	limiter := newImportExpansionLimiter()

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
func parseTarGz(data []byte, skipHeader bool) ([]map[string]string, error) {
	gzReader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer func() { _ = gzReader.Close() }()

	return parseTar(gzReader, skipHeader)
}

// parseTar extracts and parses files from a tar archive. Member bytes are
// bounded by a shared importExpansionLimiter so a malicious tar (or
// gzipped tar) cannot expand into an unbounded amount of memory.
func parseTar(r io.Reader, skipHeader bool) ([]map[string]string, error) {
	tarReader := tar.NewReader(r)
	limiter := newImportExpansionLimiter()

	var allRecords []map[string]string
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
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

		encodedOps = append(encodedOps, protocol.EncodeOp(protocol.OpMVCCPut, []byte(storedKey), value, nil, 0))
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
		cmd := protocol.EncodeBatch(chunk.ops)
		_, err := s.host.SyncPropose(ctx, s.shardID, cmd)
		if err != nil {
			if transactional {
				// For transactional mode, return the error directly.
				return nil, s.translateProposeError(err)
			}
			// For non-transactional mode, mark this chunk's rows as failed
			// and continue with the remaining chunks.
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
