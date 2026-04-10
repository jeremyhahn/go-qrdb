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

// Chunker splits text into chunks according to a strategy.
type Chunker interface {
	// Chunk divides text into segments. Returns EmptyTextError when text is
	// empty or InvalidConfigError when configuration is invalid.
	Chunk(text string) ([]Chunk, error)
}

// New constructs the Chunker selected by cfg.Strategy.
// Supported strategies: "fixed", "sentence", "paragraph".
// Returns InvalidConfigError for unknown strategies.
func New(cfg *Config) (Chunker, error) {
	if cfg == nil {
		cfg = DefaultConfig()
	}
	switch cfg.Strategy {
	case "fixed", "":
		return NewFixedChunker(cfg)
	case "sentence":
		return NewSentenceChunker(cfg)
	case "paragraph":
		return NewParagraphChunker(cfg)
	default:
		return nil, &InvalidConfigError{Reason: "unknown strategy: " + cfg.Strategy}
	}
}
