//go:build qrdb_fts

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

package fts

import (
	"encoding/binary"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// InvertedIndex stores a mapping from terms to entity keys using the
// storage.Storage abstraction. All operations go through the same storage
// layer as the DAO, enabling Raft replication for distributed FTS.
//
// Storage key format:
//
//	fts:{indexName}:term:{token}:{entityKey}{uint32BE(len(entityKey))} -> positionData
//	fts:{indexName}:doc:{entityKey} -> docMeta (term count as uint32BE)
type InvertedIndex struct {
	storage  storage.Storage
	name     string
	analyzer *Analyzer
}

// NewInvertedIndex creates an inverted index backed by the given storage.
func NewInvertedIndex(store storage.Storage, name string, analyzer *Analyzer) *InvertedIndex {
	if analyzer == nil {
		analyzer = NewAnalyzer()
	}
	return &InvertedIndex{
		storage:  store,
		name:     name,
		analyzer: analyzer,
	}
}

// Index adds or replaces the text content for an entity key.
// The text is analyzed into terms and each term is stored in the index.
func (idx *InvertedIndex) Index(entityKey []byte, text string) error {
	// Remove old entries first.
	if err := idx.Remove(entityKey); err != nil {
		return err
	}

	positions := idx.analyzer.AnalyzeWithPositions(text)
	if len(positions) == 0 {
		return nil
	}

	batch := idx.storage.Batch()
	defer func() { _ = batch.Close() }()

	// Store term → entity key mappings.
	for _, tp := range positions {
		key := idx.buildTermKey(tp.Token, entityKey)
		// Value encodes position as uint32 big-endian.
		posBytes := make([]byte, 4)
		binary.BigEndian.PutUint32(posBytes, uint32(tp.Position))
		if err := batch.Set(key, posBytes); err != nil {
			return err
		}
	}

	// Store doc metadata (term count).
	docKey := idx.buildDocKey(entityKey)
	countBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(countBytes, uint32(len(positions)))
	if err := batch.Set(docKey, countBytes); err != nil {
		return err
	}

	return batch.Commit()
}

// Remove deletes all index entries for an entity key.
func (idx *InvertedIndex) Remove(entityKey []byte) error {
	// Check if doc exists.
	docKey := idx.buildDocKey(entityKey)
	_, err := idx.storage.Get(docKey)
	if err != nil {
		return nil // not indexed; nothing to remove
	}

	// Scan all term entries for this entity and delete them.
	// We need to scan the term prefix and check each entry.
	termPrefix := []byte("fts:" + idx.name + ":term:")
	batch := idx.storage.Batch()
	defer func() { _ = batch.Close() }()

	iter := idx.storage.NewIterator(&storage.IteratorOptions{
		LowerBound: termPrefix,
		UpperBound: incrementBytes(termPrefix),
		OnlyKeys:   true,
	})
	defer func() { _ = iter.Close() }()

	for iter.SeekToFirst(); iter.Valid(); iter.Next() {
		key := iter.Key()
		// Extract entity key from the term key and check if it matches.
		ek, extractErr := extractEntityKeyFromTermKey(key)
		if extractErr != nil {
			continue
		}
		if string(ek) == string(entityKey) {
			keyCopy := make([]byte, len(key))
			copy(keyCopy, key)
			if err := batch.Delete(keyCopy); err != nil {
				return err
			}
		}
	}
	if err := iter.Error(); err != nil {
		return err
	}

	// Delete doc metadata.
	if err := batch.Delete(docKey); err != nil {
		return err
	}

	return batch.Commit()
}

// Search finds entity keys matching the query text. The query is analyzed
// using the same analyzer as indexing. By default, all terms must match
// (AND semantics). Returns entity keys in arbitrary order.
func (idx *InvertedIndex) Search(query string) ([][]byte, error) {
	terms := idx.analyzer.Analyze(query)
	if len(terms) == 0 {
		return nil, nil
	}

	// For each term, collect matching entity keys.
	var termSets []map[string][]byte

	for _, term := range terms {
		keys, err := idx.getTermKeys(term)
		if err != nil {
			return nil, err
		}
		keySet := make(map[string][]byte, len(keys))
		for _, k := range keys {
			keySet[string(k)] = k
		}
		termSets = append(termSets, keySet)
	}

	if len(termSets) == 0 {
		return nil, nil
	}

	// Intersect all term sets (AND semantics).
	result := termSets[0]
	for i := 1; i < len(termSets); i++ {
		intersected := make(map[string][]byte)
		for k, v := range result {
			if _, ok := termSets[i][k]; ok {
				intersected[k] = v
			}
		}
		result = intersected
	}

	keys := make([][]byte, 0, len(result))
	for _, k := range result {
		keys = append(keys, k)
	}
	return keys, nil
}

// SearchOr finds entity keys matching any of the query terms (OR semantics).
func (idx *InvertedIndex) SearchOr(query string) ([][]byte, error) {
	terms := idx.analyzer.Analyze(query)
	if len(terms) == 0 {
		return nil, nil
	}

	seen := make(map[string][]byte)
	for _, term := range terms {
		keys, err := idx.getTermKeys(term)
		if err != nil {
			return nil, err
		}
		for _, k := range keys {
			seen[string(k)] = k
		}
	}

	keys := make([][]byte, 0, len(seen))
	for _, k := range seen {
		keys = append(keys, k)
	}
	return keys, nil
}

// getTermKeys returns all entity keys for a given term.
func (idx *InvertedIndex) getTermKeys(term string) ([][]byte, error) {
	prefix := idx.buildTermPrefix(term)

	iter := idx.storage.NewIterator(&storage.IteratorOptions{
		LowerBound: prefix,
		UpperBound: incrementBytes(prefix),
		OnlyKeys:   true,
	})
	defer func() { _ = iter.Close() }()

	var keys [][]byte
	for iter.SeekToFirst(); iter.Valid(); iter.Next() {
		ek, err := extractEntityKeyFromTermKey(iter.Key())
		if err != nil {
			continue
		}
		keys = append(keys, ek)
	}

	if err := iter.Error(); err != nil {
		return nil, err
	}

	return keys, nil
}

// buildTermKey constructs the storage key for a term → entity mapping.
// Format: "fts:{name}:term:{token}:{entityKey}{uint32BE(len(entityKey))}"
func (idx *InvertedIndex) buildTermKey(token string, entityKey []byte) []byte {
	prefix := idx.buildTermPrefix(token)
	buf := make([]byte, 0, len(prefix)+len(entityKey)+4)
	buf = append(buf, prefix...)
	buf = append(buf, entityKey...)
	lenBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(lenBytes, uint32(len(entityKey)))
	buf = append(buf, lenBytes...)
	return buf
}

// buildTermPrefix constructs the prefix for scanning a term's entries.
// Format: "fts:{name}:term:{token}:"
func (idx *InvertedIndex) buildTermPrefix(token string) []byte {
	return []byte("fts:" + idx.name + ":term:" + token + ":")
}

// buildDocKey constructs the doc metadata key.
// Format: "fts:{name}:doc:{entityKey}"
func (idx *InvertedIndex) buildDocKey(entityKey []byte) []byte {
	return append([]byte("fts:"+idx.name+":doc:"), entityKey...)
}

// extractEntityKeyFromTermKey extracts the entity key from a term key.
// The last 4 bytes encode the entity key length.
func extractEntityKeyFromTermKey(key []byte) ([]byte, error) {
	if len(key) < 4 {
		return nil, errKeyTooShort
	}
	keyLen := binary.BigEndian.Uint32(key[len(key)-4:])
	if uint32(len(key)) < keyLen+4 {
		return nil, errKeyTooShort
	}
	ek := make([]byte, keyLen)
	copy(ek, key[len(key)-4-int(keyLen):len(key)-4])
	return ek, nil
}

// incrementBytes returns the next byte sequence for exclusive upper bounds.
func incrementBytes(b []byte) []byte {
	result := make([]byte, len(b))
	copy(result, b)
	for i := len(result) - 1; i >= 0; i-- {
		if result[i] < 0xff {
			result[i]++
			return result
		}
		result[i] = 0
	}
	return append(result, 0)
}

type ftsError string

func (e ftsError) Error() string { return string(e) }

const errKeyTooShort ftsError = "fts: key too short"
