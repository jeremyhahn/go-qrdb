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

import (
	"strings"
	"testing"
)

// benchText generates a realistic prose document of approximately targetBytes
// bytes by repeating a short sentence. The sentence is chosen to exercise
// all three chunkers: it contains sentence boundaries (periods, question marks)
// and is composed of natural paragraph breaks when assembled.
func benchText(targetBytes int) string {
	const sentence = "The quick brown fox jumps over the lazy dog near the river bank. "
	buf := strings.Builder{}
	buf.Grow(targetBytes)
	for buf.Len() < targetBytes {
		buf.WriteString(sentence)
	}
	return buf.String()[:targetBytes]
}

// benchParagraphText generates a realistic prose document with paragraph
// breaks (double newlines) so that ParagraphChunker has meaningful boundaries
// to split on.
func benchParagraphText(paragraphs, sentencesPerParagraph int) string {
	const sentence = "The quick brown fox jumps over the lazy dog. "
	buf := strings.Builder{}
	buf.Grow(paragraphs * sentencesPerParagraph * len(sentence))
	for p := 0; p < paragraphs; p++ {
		for s := 0; s < sentencesPerParagraph; s++ {
			buf.WriteString(sentence)
		}
		if p < paragraphs-1 {
			buf.WriteString("\n\n")
		}
	}
	return buf.String()
}

// benchSentenceText generates a realistic prose document with varied sentence
// terminators so that SentenceChunker exercises its boundary detection logic.
func benchSentenceText(sentenceCount int) string {
	sentences := []string{
		"The quick brown fox jumps over the lazy dog.",
		"She sells seashells by the seashore?",
		"Pack my box with five dozen liquor jugs!",
		"How vexingly quick daft zebras jump.",
		"The five boxing wizards jump quickly.",
	}
	buf := strings.Builder{}
	buf.Grow(sentenceCount * 50)
	for i := 0; i < sentenceCount; i++ {
		buf.WriteString(sentences[i%len(sentences)])
		buf.WriteByte(' ')
	}
	return buf.String()
}

// ---------------------------------------------------------------------------
// FixedChunker benchmarks
// ---------------------------------------------------------------------------

// BenchmarkFixedChunker_Chunk measures throughput when splitting a 4 KB
// document into 512-character fixed-size windows with 50-character overlap.
// This is the default configuration used by the RAG ingest pipeline.
func BenchmarkFixedChunker_Chunk(b *testing.B) {
	const docSize = 4 * 1024

	cfg := DefaultConfig() // strategy=fixed, size=512, overlap=50, minSize=100
	ch, err := NewFixedChunker(cfg)
	if err != nil {
		b.Fatalf("NewFixedChunker: %v", err)
	}

	text := benchText(docSize)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		if _, err := ch.Chunk(text); err != nil {
			b.Fatalf("Chunk: %v", err)
		}
	}
}

// BenchmarkFixedChunker_Chunk_LargeDoc measures throughput for a 64 KB
// document to show how allocation cost scales with document size.
func BenchmarkFixedChunker_Chunk_LargeDoc(b *testing.B) {
	const docSize = 64 * 1024

	cfg := DefaultConfig()
	ch, err := NewFixedChunker(cfg)
	if err != nil {
		b.Fatalf("NewFixedChunker: %v", err)
	}

	text := benchText(docSize)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		if _, err := ch.Chunk(text); err != nil {
			b.Fatalf("Chunk large: %v", err)
		}
	}
}

// BenchmarkFixedChunker_Chunk_SmallChunks measures throughput when using small
// 128-character chunks with no overlap, maximising the number of chunks
// produced and stressing slice growth and re-indexing.
func BenchmarkFixedChunker_Chunk_SmallChunks(b *testing.B) {
	const docSize = 8 * 1024

	cfg := &Config{Strategy: "fixed", ChunkSize: 128, Overlap: 0, MinSize: 0}
	ch, err := NewFixedChunker(cfg)
	if err != nil {
		b.Fatalf("NewFixedChunker: %v", err)
	}

	text := benchText(docSize)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		if _, err := ch.Chunk(text); err != nil {
			b.Fatalf("Chunk small chunks: %v", err)
		}
	}
}

// ---------------------------------------------------------------------------
// SentenceChunker benchmarks
// ---------------------------------------------------------------------------

// BenchmarkSentenceChunker_Chunk measures throughput when splitting a 200-sentence
// document using sentence boundary detection. This exercises the boundary
// detection, abbreviation lookup, and merging logic.
func BenchmarkSentenceChunker_Chunk(b *testing.B) {
	cfg := &Config{Strategy: "sentence", ChunkSize: 512, Overlap: 50, MinSize: 100}
	ch, err := NewSentenceChunker(cfg)
	if err != nil {
		b.Fatalf("NewSentenceChunker: %v", err)
	}

	text := benchSentenceText(200)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		if _, err := ch.Chunk(text); err != nil {
			b.Fatalf("Chunk: %v", err)
		}
	}
}

// BenchmarkSentenceChunker_Chunk_LargeDoc measures throughput for a
// 1000-sentence document, highlighting how sentence scanning scales with
// corpus size.
func BenchmarkSentenceChunker_Chunk_LargeDoc(b *testing.B) {
	cfg := &Config{Strategy: "sentence", ChunkSize: 512, Overlap: 50, MinSize: 100}
	ch, err := NewSentenceChunker(cfg)
	if err != nil {
		b.Fatalf("NewSentenceChunker: %v", err)
	}

	text := benchSentenceText(1000)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		if _, err := ch.Chunk(text); err != nil {
			b.Fatalf("Chunk large: %v", err)
		}
	}
}

// ---------------------------------------------------------------------------
// ParagraphChunker benchmarks
// ---------------------------------------------------------------------------

// BenchmarkParagraphChunker_Chunk measures throughput when splitting a document
// composed of 50 paragraphs with 5 sentences each. This exercises paragraph
// boundary detection, small-paragraph merging, and large-paragraph splitting
// via the internal FixedChunker fallback.
func BenchmarkParagraphChunker_Chunk(b *testing.B) {
	cfg := &Config{Strategy: "paragraph", ChunkSize: 512, Overlap: 50, MinSize: 100}
	ch, err := NewParagraphChunker(cfg)
	if err != nil {
		b.Fatalf("NewParagraphChunker: %v", err)
	}

	text := benchParagraphText(50, 5)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		if _, err := ch.Chunk(text); err != nil {
			b.Fatalf("Chunk: %v", err)
		}
	}
}

// BenchmarkParagraphChunker_Chunk_ManyParagraphs measures throughput for a
// document with 500 small paragraphs to stress the merging path that
// accumulates short paragraphs into chunks approaching ChunkSize.
func BenchmarkParagraphChunker_Chunk_ManyParagraphs(b *testing.B) {
	cfg := &Config{Strategy: "paragraph", ChunkSize: 512, Overlap: 0, MinSize: 50}
	ch, err := NewParagraphChunker(cfg)
	if err != nil {
		b.Fatalf("NewParagraphChunker: %v", err)
	}

	text := benchParagraphText(500, 1)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		if _, err := ch.Chunk(text); err != nil {
			b.Fatalf("Chunk many paragraphs: %v", err)
		}
	}
}

// ---------------------------------------------------------------------------
// New factory benchmark
// ---------------------------------------------------------------------------

// BenchmarkNew measures the overhead of the New factory function including
// strategy dispatch and config validation, to ensure the dispatch table adds
// negligible cost.
func BenchmarkNew(b *testing.B) {
	cfg := DefaultConfig()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		if _, err := New(cfg); err != nil {
			b.Fatalf("New: %v", err)
		}
	}
}
