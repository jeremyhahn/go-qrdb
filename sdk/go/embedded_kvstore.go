package sdk

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// ErrEmbeddedStorageRequired is returned by NewEmbeddedKVStore when it is
// given no storage engine to run over.
var ErrEmbeddedStorageRequired = errors.New("sdk: embedded KV store needs a storage engine")

// NewEmbeddedKVStore returns a KVStore that runs the go-qrdb KV engine
// in-process over stg, with secondary indexes maintained atomically on every
// Put and Delete. It is the store an embedding application hands to NewDAO
// when it owns its storage (for example sdkstorage.NewPebbleStorage) and runs
// no server.
//
// Get reports an absent key as (nil, nil); every other storage failure is
// returned. A query on an index that was never registered reports no matches.
// A definition the engine cannot maintain exactly (a caller-qualified index
// name, a key prefix other than "{entityType}/", or a numeric index) is
// refused with ErrKVIndexUnsupported, so a DAO downgrades that query kind to
// a full scan instead of querying an index nobody built.
func NewEmbeddedKVStore(stg storage.Storage) (KVStore, error) {
	if stg == nil {
		return nil, ErrEmbeddedStorageRequired
	}
	return &embeddedKVStore{stg: stg, eng: engine.NewKVEngine(stg)}, nil
}

// embeddedKVStore is the KVStore NewEmbeddedKVStore returns.
type embeddedKVStore struct {
	stg storage.Storage
	eng *engine.KVEngine
}

func (s *embeddedKVStore) Put(ctx context.Context, key string, value []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.eng.PutWithIndexes([]byte(key), value)
}

func (s *embeddedKVStore) Get(ctx context.Context, key string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	value, err := s.stg.Get([]byte(key))
	if err != nil {
		var notFound *storage.KeyNotFoundError
		if errors.As(err, &notFound) || errors.Is(err, storage.ErrKeyNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return value, nil
}

func (s *embeddedKVStore) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.eng.DeleteWithIndexes([]byte(key))
}

func (s *embeddedKVStore) Scan(ctx context.Context, prefix string, fn func(key string, value []byte) error) error {
	opts := &storage.IteratorOptions{LowerBound: []byte(prefix), UpperBound: prefixUpperBound([]byte(prefix))}
	it := s.stg.NewIterator(opts)
	defer func() { _ = it.Close() }()
	for ok := it.SeekToFirst(); ok && it.Valid(); ok = it.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		key := string(it.Key())
		value := append([]byte(nil), it.Value()...)
		if err := fn(key, value); err != nil {
			return err
		}
	}
	return it.Error()
}

func (s *embeddedKVStore) List(ctx context.Context, prefix string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, err := s.stg.List([]byte(prefix))
	if err != nil {
		return nil, err
	}
	keys := make([]string, len(raw))
	for i, k := range raw {
		keys[i] = string(k)
	}
	return keys, nil
}

func (s *embeddedKVStore) Exists(ctx context.Context, key string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return s.stg.Exists([]byte(key))
}

// RegisterEntityIndexes maps each definition onto the engine's and registers
// them; see NewEmbeddedKVStore for the definitions it refuses.
func (s *embeddedKVStore) RegisterEntityIndexes(entityType string, indexes []kvstore.EntityIndex) error {
	defs := make([]engine.EntityIndex, 0, len(indexes))
	for _, def := range indexes {
		if err := embeddedIndexSupported(entityType, def); err != nil {
			return err
		}
		defs = append(defs, engine.EntityIndex{
			FieldName:      def.FieldName,
			JSONFieldName:  def.JSONFieldName,
			FieldNames:     def.FieldNames,
			JSONFieldNames: def.JSONFieldNames,
			Unique:         def.Unique,
			Sparse:         def.Sparse,
			CaseSensitive:  def.CaseSensitive,
		})
	}
	return s.eng.RegisterEntityType(entityType, defs)
}

// embeddedIndexSupported refuses a definition the engine would build under a
// different name or scope than the caller will query it by.
func embeddedIndexSupported(entityType string, def kvstore.EntityIndex) error {
	if def.Numeric {
		return fmt.Errorf("index %s.%s is numeric: %w", entityType, def.FieldName, kvstore.ErrIndexUnsupported)
	}
	if def.KeyPrefix != "" && def.KeyPrefix != entityType+"/" {
		return fmt.Errorf("index key prefix %q is not %q: %w", def.KeyPrefix, entityType+"/", kvstore.ErrIndexUnsupported)
	}
	if def.IndexName == "" {
		return nil
	}
	field := def.FieldName
	if len(def.FieldNames) > 0 {
		field = strings.Join(def.FieldNames, "_")
	}
	if want := entityType + "." + field; def.IndexName != want {
		return fmt.Errorf("index name %q is not %q: %w", def.IndexName, want, kvstore.ErrIndexUnsupported)
	}
	return nil
}

func (s *embeddedKVStore) QueryIndex(ctx context.Context, indexName string, value []byte) ([][]byte, error) {
	return embeddedKeys(s.eng.Lookup(ctx, index.IndexQueryRequest{IndexName: indexName, Value: value}))
}

func (s *embeddedKVStore) ScanIndex(ctx context.Context, indexName string, start, end []byte) (map[string][][]byte, error) {
	return embeddedScan(s.eng.Lookup(ctx, index.IndexScanRequest{IndexName: indexName, Start: start, End: end}))
}

func (s *embeddedKVStore) QueryCompoundIndex(ctx context.Context, indexName string, values [][]byte) ([][]byte, error) {
	return embeddedKeys(s.eng.Lookup(ctx, index.CompoundIndexQueryRequest{IndexName: indexName, Values: values}))
}

func (s *embeddedKVStore) ScanCompoundIndex(ctx context.Context, indexName string, prefixValues [][]byte, rangeStart, rangeEnd []byte) (map[string][][]byte, error) {
	return embeddedScan(s.eng.Lookup(ctx, index.CompoundIndexScanRequest{
		IndexName:    indexName,
		PrefixValues: prefixValues,
		RangeStart:   rangeStart,
		RangeEnd:     rangeEnd,
	}))
}

// isMissingIndex reports whether err says the index was never registered.
func isMissingIndex(err error) bool {
	var missing *index.NotFoundError
	return errors.As(err, &missing)
}

func embeddedKeys(result interface{}, err error) ([][]byte, error) {
	if err != nil {
		if isMissingIndex(err) {
			return [][]byte{}, nil
		}
		return nil, err
	}
	if result == nil {
		return [][]byte{}, nil
	}
	keys, ok := result.([][]byte)
	if !ok {
		return nil, fmt.Errorf("sdk: index query returned %T, want [][]byte", result)
	}
	return keys, nil
}

func embeddedScan(result interface{}, err error) (map[string][][]byte, error) {
	if err != nil {
		if isMissingIndex(err) {
			return map[string][][]byte{}, nil
		}
		return nil, err
	}
	if result == nil {
		return map[string][][]byte{}, nil
	}
	scan, ok := result.(map[string][][]byte)
	if !ok {
		return nil, fmt.Errorf("sdk: index scan returned %T, want map[string][][]byte", result)
	}
	return scan, nil
}

// prefixUpperBound returns the smallest key greater than every key with the
// given prefix, or nil when no such key exists (empty or all-0xff prefix).
func prefixUpperBound(prefix []byte) []byte {
	end := append([]byte(nil), prefix...)
	for i := len(end) - 1; i >= 0; i-- {
		if end[i] < 0xff {
			end[i]++
			return end[:i+1]
		}
	}
	return nil
}
