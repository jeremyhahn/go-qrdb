//go:build codec_json

// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-dragondb.
//
// go-dragondb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package dao

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// benchEntity is a test entity with realistic field sizes for benchmarking.
type benchEntity struct {
	ID    uint64 `json:"id" yaml:"id" toml:"id" bson:"id" msgpack:"id"`
	Name  string `json:"name" yaml:"name" toml:"name" bson:"name" msgpack:"name"`
	Email string `json:"email" yaml:"email" toml:"email" bson:"email" msgpack:"email"`
	Bio   string `json:"bio" yaml:"bio" toml:"bio" bson:"bio" msgpack:"bio"`
}

func (e *benchEntity) EntityID() uint64      { return e.ID }
func (e *benchEntity) SetEntityID(id uint64) { e.ID = id }

// newBenchEntity creates a benchmark entity with the given ID and payload size.
func newBenchEntity(id uint64, bioSize int) *benchEntity {
	bio := make([]byte, bioSize)
	for i := range bio {
		bio[i] = 'a' + byte(i%26)
	}
	return &benchEntity{
		ID:    id,
		Name:  fmt.Sprintf("user-%d", id),
		Email: fmt.Sprintf("user-%d@example.com", id),
		Bio:   string(bio),
	}
}

// newBenchDAO creates a GenericDAO backed by an in-memory KV store for benchmarks.
func newBenchDAO(b *testing.B, codecType CodecType) GenericDAO[*benchEntity] {
	b.Helper()
	store := newMemoryKVStore()
	d, err := New[*benchEntity](
		store,
		"bench_entities",
		func() *benchEntity { return &benchEntity{} },
		WithCodec(codecType),
	)
	require.NoError(b, err)
	return d
}

// populateBenchDAO inserts n entities into the DAO and returns the DAO.
func populateBenchDAO(b *testing.B, n int, codecType CodecType) GenericDAO[*benchEntity] {
	b.Helper()
	d := newBenchDAO(b, codecType)
	ctx := context.Background()
	for i := 1; i <= n; i++ {
		entity := newBenchEntity(uint64(i), 128)
		if err := d.Save(ctx, entity); err != nil {
			b.Fatal(err)
		}
	}
	return d
}

// =============================================================================
// Codec Serialization Benchmarks
// =============================================================================

// BenchmarkCodec_Marshal benchmarks entity serialization across available codecs.
func BenchmarkCodec_Marshal(b *testing.B) {
	codecs := []struct {
		name      string
		codecType CodecType
	}{
		{"JSON", CodecJSON},
		{"YAML", CodecYAML},
		{"TOML", CodecTOML},
		{"MsgPack", CodecMsgPack},
		{"BSON", CodecBSON},
	}

	entity := newBenchEntity(1, 256)

	for _, tc := range codecs {
		if !IsCodecSupported(tc.codecType) {
			continue
		}
		b.Run(tc.name, func(b *testing.B) {
			c, err := NewCodec[*benchEntity](tc.codecType)
			require.NoError(b, err)

			// Marshal once to determine byte size for throughput calculation.
			sample, err := c.Marshal(entity)
			require.NoError(b, err)
			b.SetBytes(int64(len(sample)))

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				_, err := c.Marshal(entity)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkCodec_Unmarshal benchmarks entity deserialization across available codecs.
func BenchmarkCodec_Unmarshal(b *testing.B) {
	codecs := []struct {
		name      string
		codecType CodecType
	}{
		{"JSON", CodecJSON},
		{"YAML", CodecYAML},
		{"TOML", CodecTOML},
		{"MsgPack", CodecMsgPack},
		{"BSON", CodecBSON},
	}

	entity := newBenchEntity(1, 256)

	for _, tc := range codecs {
		if !IsCodecSupported(tc.codecType) {
			continue
		}
		b.Run(tc.name, func(b *testing.B) {
			c, err := NewCodec[*benchEntity](tc.codecType)
			require.NoError(b, err)

			data, err := c.Marshal(entity)
			require.NoError(b, err)
			b.SetBytes(int64(len(data)))

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				target := &benchEntity{}
				if err := c.Unmarshal(data, &target); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkCodec_RoundTrip benchmarks a full marshal+unmarshal cycle per codec.
func BenchmarkCodec_RoundTrip(b *testing.B) {
	codecs := []struct {
		name      string
		codecType CodecType
	}{
		{"JSON", CodecJSON},
		{"MsgPack", CodecMsgPack},
		{"BSON", CodecBSON},
	}

	entity := newBenchEntity(1, 256)

	for _, tc := range codecs {
		if !IsCodecSupported(tc.codecType) {
			continue
		}
		b.Run(tc.name, func(b *testing.B) {
			c, err := NewCodec[*benchEntity](tc.codecType)
			require.NoError(b, err)

			sample, err := c.Marshal(entity)
			require.NoError(b, err)
			b.SetBytes(int64(len(sample)))

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				data, err := c.Marshal(entity)
				if err != nil {
					b.Fatal(err)
				}
				target := &benchEntity{}
				if err := c.Unmarshal(data, &target); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkCodec_Marshal_PayloadSizes benchmarks serialization with varying payload sizes.
func BenchmarkCodec_Marshal_PayloadSizes(b *testing.B) {
	sizes := []struct {
		name string
		size int
	}{
		{"64B", 64},
		{"256B", 256},
		{"1KB", 1024},
		{"4KB", 4096},
		{"16KB", 16384},
	}

	for _, sz := range sizes {
		b.Run(sz.name, func(b *testing.B) {
			c, err := NewCodec[*benchEntity](CodecJSON)
			require.NoError(b, err)

			entity := newBenchEntity(1, sz.size)
			sample, err := c.Marshal(entity)
			require.NoError(b, err)
			b.SetBytes(int64(len(sample)))

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				_, err := c.Marshal(entity)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// =============================================================================
// GenericDAO CRUD Benchmarks
// =============================================================================

// BenchmarkDAO_Save benchmarks single entity save operations.
func BenchmarkDAO_Save(b *testing.B) {
	d := newBenchDAO(b, CodecJSON)
	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		entity := newBenchEntity(uint64(i+1), 128)
		if err := d.Save(ctx, entity); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDAO_Save_LargeEntity benchmarks saving entities with large payloads.
func BenchmarkDAO_Save_LargeEntity(b *testing.B) {
	d := newBenchDAO(b, CodecJSON)
	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		entity := newBenchEntity(uint64(i+1), 4096)
		if err := d.Save(ctx, entity); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDAO_Get benchmarks entity retrieval by ID.
func BenchmarkDAO_Get(b *testing.B) {
	numEntities := 1000
	d := populateBenchDAO(b, numEntities, CodecJSON)
	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		id := uint64(i%numEntities) + 1
		_, err := d.Get(ctx, id)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDAO_Delete benchmarks entity deletion.
func BenchmarkDAO_Delete(b *testing.B) {
	d := newBenchDAO(b, CodecJSON)
	ctx := context.Background()

	// Pre-populate entities to delete.
	for i := 1; i <= b.N; i++ {
		entity := newBenchEntity(uint64(i), 128)
		if err := d.Save(ctx, entity); err != nil {
			b.Fatal(err)
		}
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 1; i <= b.N; i++ {
		entity := &benchEntity{ID: uint64(i)}
		if err := d.Delete(ctx, entity); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDAO_Count benchmarks counting entities at varying dataset sizes.
func BenchmarkDAO_Count(b *testing.B) {
	sizes := []int{100, 1000, 10000}

	for _, sz := range sizes {
		b.Run(fmt.Sprintf("Entities-%d", sz), func(b *testing.B) {
			d := populateBenchDAO(b, sz, CodecJSON)
			ctx := context.Background()

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				_, err := d.Count(ctx)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkDAO_SaveGet_RoundTrip benchmarks a full save-then-get cycle.
func BenchmarkDAO_SaveGet_RoundTrip(b *testing.B) {
	d := newBenchDAO(b, CodecJSON)
	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		entity := newBenchEntity(uint64(i+1), 128)
		if err := d.Save(ctx, entity); err != nil {
			b.Fatal(err)
		}
		_, err := d.Get(ctx, entity.EntityID())
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDAO_Save_CodecComparison compares Save performance across codecs.
func BenchmarkDAO_Save_CodecComparison(b *testing.B) {
	codecs := []struct {
		name      string
		codecType CodecType
	}{
		{"JSON", CodecJSON},
		{"MsgPack", CodecMsgPack},
		{"BSON", CodecBSON},
	}

	for _, tc := range codecs {
		if !IsCodecSupported(tc.codecType) {
			continue
		}
		b.Run(tc.name, func(b *testing.B) {
			d := newBenchDAO(b, tc.codecType)
			ctx := context.Background()

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				entity := newBenchEntity(uint64(i+1), 128)
				if err := d.Save(ctx, entity); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkDAO_Get_CodecComparison compares Get performance across codecs.
func BenchmarkDAO_Get_CodecComparison(b *testing.B) {
	codecs := []struct {
		name      string
		codecType CodecType
	}{
		{"JSON", CodecJSON},
		{"MsgPack", CodecMsgPack},
		{"BSON", CodecBSON},
	}

	numEntities := 1000

	for _, tc := range codecs {
		if !IsCodecSupported(tc.codecType) {
			continue
		}
		b.Run(tc.name, func(b *testing.B) {
			d := populateBenchDAO(b, numEntities, tc.codecType)
			ctx := context.Background()

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				id := uint64(i%numEntities) + 1
				_, err := d.Get(ctx, id)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// =============================================================================
// Pagination Benchmarks
// =============================================================================

// BenchmarkDAO_Page benchmarks single page retrieval at varying dataset sizes.
func BenchmarkDAO_Page(b *testing.B) {
	sizes := []int{100, 1000, 10000}

	for _, sz := range sizes {
		b.Run(fmt.Sprintf("Entities-%d", sz), func(b *testing.B) {
			d := populateBenchDAO(b, sz, CodecJSON)
			ctx := context.Background()
			query := PageQuery{Page: 1, PageSize: 25}

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				_, err := d.Page(ctx, query)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkDAO_Page_VaryingPageSize benchmarks pagination with different page sizes.
func BenchmarkDAO_Page_VaryingPageSize(b *testing.B) {
	numEntities := 1000
	pageSizes := []int{10, 25, 50, 100}

	for _, ps := range pageSizes {
		b.Run(fmt.Sprintf("PageSize-%d", ps), func(b *testing.B) {
			d := populateBenchDAO(b, numEntities, CodecJSON)
			ctx := context.Background()
			query := PageQuery{Page: 1, PageSize: ps}

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				_, err := d.Page(ctx, query)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkDAO_Page_MiddlePage benchmarks retrieving a page from the middle of a dataset.
func BenchmarkDAO_Page_MiddlePage(b *testing.B) {
	sizes := []int{100, 1000, 10000}

	for _, sz := range sizes {
		b.Run(fmt.Sprintf("Entities-%d", sz), func(b *testing.B) {
			d := populateBenchDAO(b, sz, CodecJSON)
			ctx := context.Background()

			// Request a page from the middle of the dataset.
			totalPages := sz / 25
			middlePage := totalPages / 2
			if middlePage < 1 {
				middlePage = 1
			}
			query := PageQuery{Page: middlePage, PageSize: 25}

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				_, err := d.Page(ctx, query)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkDAO_ForEachPage benchmarks iterating all pages at varying dataset sizes.
func BenchmarkDAO_ForEachPage(b *testing.B) {
	sizes := []int{100, 1000, 10000}

	for _, sz := range sizes {
		b.Run(fmt.Sprintf("Entities-%d", sz), func(b *testing.B) {
			d := populateBenchDAO(b, sz, CodecJSON)
			ctx := context.Background()
			query := PageQuery{Page: 1, PageSize: 50}

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				err := d.ForEachPage(ctx, query, func(result PageResult[*benchEntity]) error {
					return nil
				})
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// =============================================================================
// Parallel (Concurrent) Benchmarks
// =============================================================================

// BenchmarkDAO_Save_Parallel benchmarks concurrent save operations.
func BenchmarkDAO_Save_Parallel(b *testing.B) {
	d := newBenchDAO(b, CodecJSON)
	ctx := context.Background()
	var counter atomic.Uint64

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			id := counter.Add(1)
			entity := newBenchEntity(id, 128)
			if err := d.Save(ctx, entity); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkDAO_Get_Parallel benchmarks concurrent get operations.
func BenchmarkDAO_Get_Parallel(b *testing.B) {
	numEntities := 1000
	d := populateBenchDAO(b, numEntities, CodecJSON)
	ctx := context.Background()
	var counter atomic.Uint64

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			id := counter.Add(1)
			entityID := (id % uint64(numEntities)) + 1
			_, err := d.Get(ctx, entityID)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkDAO_SaveGet_Parallel benchmarks concurrent mixed read/write operations.
func BenchmarkDAO_SaveGet_Parallel(b *testing.B) {
	numEntities := 1000
	d := populateBenchDAO(b, numEntities, CodecJSON)
	ctx := context.Background()
	var counter atomic.Uint64

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			id := counter.Add(1)
			if id%2 == 0 {
				// Write path
				entity := newBenchEntity(uint64(numEntities)+id, 128)
				if err := d.Save(ctx, entity); err != nil {
					b.Fatal(err)
				}
			} else {
				// Read path
				entityID := (id % uint64(numEntities)) + 1
				_, err := d.Get(ctx, entityID)
				if err != nil {
					b.Fatal(err)
				}
			}
		}
	})
}

// BenchmarkDAO_Page_Parallel benchmarks concurrent page retrieval.
func BenchmarkDAO_Page_Parallel(b *testing.B) {
	numEntities := 1000
	d := populateBenchDAO(b, numEntities, CodecJSON)
	ctx := context.Background()
	var counter atomic.Uint64

	totalPages := numEntities / 25
	if totalPages < 1 {
		totalPages = 1
	}

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			id := counter.Add(1)
			page := int(id%uint64(totalPages)) + 1
			query := PageQuery{Page: page, PageSize: 25}
			_, err := d.Page(ctx, query)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkDAO_Count_Parallel benchmarks concurrent count operations.
func BenchmarkDAO_Count_Parallel(b *testing.B) {
	numEntities := 1000
	d := populateBenchDAO(b, numEntities, CodecJSON)
	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, err := d.Count(ctx)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkDAO_Delete_Parallel benchmarks concurrent delete operations.
func BenchmarkDAO_Delete_Parallel(b *testing.B) {
	d := newBenchDAO(b, CodecJSON)
	ctx := context.Background()
	var counter atomic.Uint64

	// Pre-populate a large pool of entities for deletion.
	populateCount := b.N + 10000
	for i := 1; i <= populateCount; i++ {
		entity := newBenchEntity(uint64(i), 128)
		if err := d.Save(ctx, entity); err != nil {
			b.Fatal(err)
		}
	}

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			id := counter.Add(1)
			entity := &benchEntity{ID: id}
			if err := d.Delete(ctx, entity); err != nil {
				b.Fatal(err)
			}
		}
	})
}
