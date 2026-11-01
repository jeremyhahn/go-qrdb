package vector

import (
	"context"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

// scanOrderVectors is the size of the namespace each paging test walks.
const scanOrderVectors = 2500

// scanOrderPageSizes are the page sizes each paging test walks the
// namespace at: one vector, a size that divides nothing, a typical rebuild
// page, and the whole namespace in one page.
var scanOrderPageSizes = []int{1, 7, 1000, scanOrderVectors}

// scanOrderID spaces the IDs three apart, so a FromID can fall in a gap.
func scanOrderID(i int) uint64 { return uint64(i)*3 + 1 }

// scanOrderLatest is the latest version written for the i-th vector: every
// fifth one is written three times.
func scanOrderLatest(i int) uint64 {
	if i%5 == 0 {
		return 3
	}
	return 1
}

// fillScanOrderNamespace writes scanOrderVectors vectors to ns, every fifth
// in three versions, in batches whose order is not the IDs' order.
func fillScanOrderNamespace(t *testing.T, s Storage, ns string) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, s.CreateNamespace(ctx, &NamespaceInfo{Name: ns, Dimension: 2}))
	for version := uint64(1); version <= 3; version++ {
		batch := make([]*VectorValue, 0, scanOrderVectors)
		for i := scanOrderVectors - 1; i >= 0; i-- {
			if version > scanOrderLatest(i) {
				continue
			}
			batch = append(batch, &VectorValue{
				ID:        scanOrderID(i),
				Version:   version,
				Embedding: []float32{float32(i), float32(version)},
			})
		}
		require.NoError(t, s.BatchUpsertVectors(ctx, ns, batch))
	}
}

// pageNamespace walks ns page by page, resuming each page at the last ID
// plus one, and returns every vector seen. It fails if a page is longer
// than size, or if the walk takes more pages than the namespace can fill.
func pageNamespace(t *testing.T, s Storage, ns string, size int) []*VectorValue {
	t.Helper()
	var (
		seen []*VectorValue
		from uint64
	)
	for pages := 0; ; pages++ {
		require.LessOrEqual(t, pages, scanOrderVectors/size+1, "page size %d: the walk did not end", size)
		page, err := s.ScanNamespace(context.Background(), ns, &ScanOptions{FromID: from, Limit: size})
		require.NoError(t, err)
		require.LessOrEqual(t, len(page), size)
		seen = append(seen, page...)
		if len(page) < size {
			return seen
		}
		last := page[len(page)-1].ID
		if last == math.MaxUint64 {
			return seen
		}
		from = last + 1
	}
}

// requireEveryVectorOnceInOrder fails unless seen is the namespace written
// by fillScanOrderNamespace: every ID once, ascending, at its latest version.
func requireEveryVectorOnceInOrder(t *testing.T, seen []*VectorValue, size int) {
	t.Helper()
	require.Len(t, seen, scanOrderVectors, "page size %d", size)
	for i, vec := range seen {
		require.Equal(t, scanOrderID(i), vec.ID, "page size %d: position %d", size, i)
		require.Equal(t, scanOrderLatest(i), vec.Version, "page size %d: ID %d", size, vec.ID)
	}
}

// scanOrderStores builds each production storage the paging tests run
// against, in memory.
func scanOrderStores(t *testing.T) map[string]Storage {
	t.Helper()
	pebbleStore, err := NewPebbleVectorStorage(DefaultConfig("/vec"), &testLogger{}, inMemory)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pebbleStore.Close() })
	memoryStore := NewMemoryVectorStorage()
	t.Cleanup(func() { _ = memoryStore.Close() })
	return map[string]Storage{"pebble": pebbleStore, "memory": memoryStore}
}

func TestScanNamespace_PagesEveryVectorOnceInAscendingIDOrder(t *testing.T) {
	for name, s := range scanOrderStores(t) {
		t.Run(name, func(t *testing.T) {
			fillScanOrderNamespace(t, s, "docs")
			for _, size := range scanOrderPageSizes {
				requireEveryVectorOnceInOrder(t, pageNamespace(t, s, "docs", size), size)
			}
		})
	}
}

func TestScanNamespace_FromIDIsAnInclusiveLowerBound(t *testing.T) {
	for name, s := range scanOrderStores(t) {
		t.Run(name, func(t *testing.T) {
			fillScanOrderNamespace(t, s, "docs")
			ctx := context.Background()

			onID, err := s.ScanNamespace(ctx, "docs", &ScanOptions{FromID: scanOrderID(10), Limit: 2})
			require.NoError(t, err)
			require.Equal(t, []uint64{scanOrderID(10), scanOrderID(11)}, scanOrderIDs(onID))

			inGap, err := s.ScanNamespace(ctx, "docs", &ScanOptions{FromID: scanOrderID(10) + 1, Limit: 2})
			require.NoError(t, err)
			require.Equal(t, []uint64{scanOrderID(11), scanOrderID(12)}, scanOrderIDs(inGap))
		})
	}
}

func TestScanNamespace_FromIDPastTheLastIDIsEmpty(t *testing.T) {
	for name, s := range scanOrderStores(t) {
		t.Run(name, func(t *testing.T) {
			fillScanOrderNamespace(t, s, "docs")
			page, err := s.ScanNamespace(context.Background(), "docs",
				&ScanOptions{FromID: scanOrderID(scanOrderVectors-1) + 1, Limit: 10})
			require.NoError(t, err)
			require.Empty(t, page)
		})
	}
}

func TestScanNamespace_MaxUint64IsTheLastID(t *testing.T) {
	for name, s := range scanOrderStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			require.NoError(t, s.CreateNamespace(ctx, &NamespaceInfo{Name: "edge", Dimension: 2}))
			require.NoError(t, s.BatchUpsertVectors(ctx, "edge", []*VectorValue{
				{ID: math.MaxUint64, Version: 1, Embedding: []float32{1, 2}},
				{ID: 1, Version: 1, Embedding: []float32{3, 4}},
			}))

			first, err := s.ScanNamespace(ctx, "edge", &ScanOptions{Limit: 1})
			require.NoError(t, err)
			require.Equal(t, []uint64{1}, scanOrderIDs(first))

			last, err := s.ScanNamespace(ctx, "edge", &ScanOptions{FromID: 2, Limit: 1})
			require.NoError(t, err)
			require.Equal(t, []uint64{math.MaxUint64}, scanOrderIDs(last))
		})
	}
}

func TestScanNamespace_OffsetSkipsInAscendingIDOrderAfterFromID(t *testing.T) {
	for name, s := range scanOrderStores(t) {
		t.Run(name, func(t *testing.T) {
			fillScanOrderNamespace(t, s, "docs")
			ctx := context.Background()

			page, err := s.ScanNamespace(ctx, "docs", &ScanOptions{FromID: scanOrderID(4), Offset: 3, Limit: 2})
			require.NoError(t, err)
			require.Equal(t, []uint64{scanOrderID(7), scanOrderID(8)}, scanOrderIDs(page))

			past, err := s.ScanNamespace(ctx, "docs", &ScanOptions{Offset: scanOrderVectors})
			require.NoError(t, err)
			require.Empty(t, past, "an offset at the end must give an empty page, not the whole namespace")
		})
	}
}

func TestScanNamespace_WithoutLimitReturnsTheRestInOrder(t *testing.T) {
	for name, s := range scanOrderStores(t) {
		t.Run(name, func(t *testing.T) {
			fillScanOrderNamespace(t, s, "docs")
			all, err := s.ScanNamespace(context.Background(), "docs", nil)
			require.NoError(t, err)
			requireEveryVectorOnceInOrder(t, all, 0)

			rest, err := s.ScanNamespace(context.Background(), "docs", &ScanOptions{FromID: scanOrderID(scanOrderVectors - 2)})
			require.NoError(t, err)
			require.Equal(t, []uint64{scanOrderID(scanOrderVectors - 2), scanOrderID(scanOrderVectors - 1)}, scanOrderIDs(rest))
		})
	}
}

func scanOrderIDs(vecs []*VectorValue) []uint64 {
	ids := make([]uint64, len(vecs))
	for i, vec := range vecs {
		ids[i] = vec.ID
	}
	return ids
}
