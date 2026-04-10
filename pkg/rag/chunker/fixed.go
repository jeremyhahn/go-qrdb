//go:build qrdb_vector

// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package chunker

// FixedChunker splits text into fixed-size character windows with configurable
// overlap between adjacent chunks.
type FixedChunker struct {
	config *Config
}

// Compile-time assertion that FixedChunker implements Chunker.
var _ Chunker = (*FixedChunker)(nil)

// NewFixedChunker constructs a FixedChunker and validates cfg.
// Returns InvalidConfigError when ChunkSize <= 0, Overlap < 0,
// Overlap >= ChunkSize, or MinSize < 0.
func NewFixedChunker(cfg *Config) (*FixedChunker, error) {
	if cfg == nil {
		cfg = DefaultConfig()
	}
	if cfg.ChunkSize <= 0 {
		return nil, &InvalidConfigError{Reason: "chunk_size must be > 0"}
	}
	if cfg.Overlap < 0 {
		return nil, &InvalidConfigError{Reason: "overlap must be >= 0"}
	}
	if cfg.Overlap >= cfg.ChunkSize {
		return nil, &InvalidConfigError{Reason: "overlap must be < chunk_size"}
	}
	if cfg.MinSize < 0 {
		return nil, &InvalidConfigError{Reason: "min_size must be >= 0"}
	}
	return &FixedChunker{config: cfg}, nil
}

// Chunk splits text into fixed-size windows. Adjacent chunks overlap by
// config.Overlap characters. When the final window is smaller than MinSize
// it is merged into the preceding chunk. Returns EmptyTextError when text
// is empty.
func (c *FixedChunker) Chunk(text string) ([]Chunk, error) {
	if text == "" {
		return nil, &EmptyTextError{}
	}

	runes := []rune(text)
	total := len(runes)
	step := c.config.ChunkSize - c.config.Overlap
	if step <= 0 {
		step = 1
	}

	chunks := make([]Chunk, 0, total/step+1)
	for start := 0; start < total; start += step {
		end := start + c.config.ChunkSize
		if end > total {
			end = total
		}
		seg := string(runes[start:end])
		byteOffset := len(string(runes[:start]))
		ch := Chunk{
			Text:   seg,
			Index:  len(chunks),
			Offset: byteOffset,
			Length: len(seg),
		}
		chunks = append(chunks, ch)
		if end == total {
			break
		}
	}

	// Merge a trailing chunk that is below MinSize into the previous chunk.
	if c.config.MinSize > 0 && len(chunks) > 1 {
		last := &chunks[len(chunks)-1]
		if len([]rune(last.Text)) < c.config.MinSize {
			prev := &chunks[len(chunks)-2]
			merged := prev.Text + last.Text
			prev.Text = merged
			prev.Length = len(merged)
			chunks = chunks[:len(chunks)-1]
		}
	}

	// Re-index to keep ordinals contiguous after any merge.
	for i := range chunks {
		chunks[i].Index = i
	}

	return chunks, nil
}
