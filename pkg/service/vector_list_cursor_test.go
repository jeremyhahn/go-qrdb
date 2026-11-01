package service

import (
	"context"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// listCursorVectors is the size of the namespace the cursor tests page.
const listCursorVectors = 2500

// newListCursorService builds a vector service over the in-memory store,
// with namespace ns holding the given IDs, written in descending order and
// each in two versions, so neither write order nor versions can pass for
// ID order.
func newListCursorService(t *testing.T, ns string, ids []uint64) (*vectorService, vector.Storage) {
	t.Helper()
	storage := vector.NewMemoryVectorStorage()
	t.Cleanup(func() { _ = storage.Close() })
	ctx := context.Background()
	scoped := scopedNamespace(tenantid.System, ns)
	require.NoError(t, storage.CreateNamespace(ctx, &vector.NamespaceInfo{Name: scoped, Dimension: 2}))
	for version := uint64(1); version <= 2; version++ {
		batch := make([]*vector.VectorValue, 0, len(ids))
		for i := len(ids) - 1; i >= 0; i-- {
			batch = append(batch, &vector.VectorValue{
				ID:        ids[i],
				Version:   version,
				Embedding: []float32{float32(i), float32(version)},
			})
		}
		require.NoError(t, storage.BatchUpsertVectors(ctx, scoped, batch))
	}
	svc, err := NewVectorService(storage, newVecTestLogger())
	require.NoError(t, err)
	return svc.(*vectorService), storage
}

// listCursorIDs is listCursorVectors IDs spaced three apart.
func listCursorIDs() []uint64 {
	ids := make([]uint64, listCursorVectors)
	for i := range ids {
		ids[i] = uint64(i)*3 + 1
	}
	return ids
}

// listAll pages ns through ListVectors at the given page size, following
// NextCursor, and returns every ID seen in order.
func listAll(t *testing.T, svc VectorService, ns string, size int) []uint64 {
	t.Helper()
	var (
		seen   []uint64
		cursor string
	)
	for pages := 0; ; pages++ {
		require.LessOrEqual(t, pages, listCursorVectors/size+1, "page size %d: the walk did not end", size)
		resp, err := svc.ListVectors(context.Background(), &ListVectorsRequest{Namespace: ns, Limit: size, Cursor: cursor})
		require.NoError(t, err)
		require.LessOrEqual(t, len(resp.Vectors), size)
		for _, vec := range resp.Vectors {
			seen = append(seen, vec.ID)
		}
		if resp.NextCursor == "" {
			return seen
		}
		cursor = resp.NextCursor
	}
}

func TestListVectors_CursorPagesEveryIDOnceInOrder(t *testing.T) {
	ids := listCursorIDs()
	svc, _ := newListCursorService(t, "docs", ids)
	for _, size := range []int{1, 7, 1000} {
		require.Equal(t, ids, listAll(t, svc, "docs", size), "page size %d", size)
	}
}

func TestListVectors_NextCursorIsOnePastTheLastID(t *testing.T) {
	ids := listCursorIDs()
	svc, _ := newListCursorService(t, "docs", ids)

	resp, err := svc.ListVectors(context.Background(), &ListVectorsRequest{Namespace: "docs", Limit: 2})
	require.NoError(t, err)
	require.Equal(t, strconv.FormatUint(ids[1]+1, 10), resp.NextCursor)

	// A full last page has no vector after it, so no cursor either.
	resp, err = svc.ListVectors(context.Background(), &ListVectorsRequest{
		Namespace: "docs", Limit: 2, Cursor: strconv.FormatUint(ids[listCursorVectors-2], 10),
	})
	require.NoError(t, err)
	require.Len(t, resp.Vectors, 2)
	require.Empty(t, resp.NextCursor)
}

func TestListVectors_MalformedCursorIsRefused(t *testing.T) {
	svc, _ := newListCursorService(t, "docs", listCursorIDs())
	for _, cursor := range []string{"abc", "-1", "1.5", "18446744073709551616", " 7"} {
		resp, err := svc.ListVectors(context.Background(), &ListVectorsRequest{Namespace: "docs", Cursor: cursor})
		require.Nil(t, resp, "cursor %q", cursor)
		requireErrorAs[*InvalidVectorError](t, err)
	}
}

func TestListVectors_CursorPastTheEndIsAnEmptyLastPage(t *testing.T) {
	ids := listCursorIDs()
	svc, _ := newListCursorService(t, "docs", ids)
	resp, err := svc.ListVectors(context.Background(), &ListVectorsRequest{
		Namespace: "docs", Limit: 10, Cursor: strconv.FormatUint(ids[listCursorVectors-1]+1, 10),
	})
	require.NoError(t, err)
	require.Empty(t, resp.Vectors)
	require.Empty(t, resp.NextCursor)
}

func TestListVectors_MaxUint64LastIDEndsTheWalk(t *testing.T) {
	ids := []uint64{1, 2, math.MaxUint64}
	svc, _ := newListCursorService(t, "edge", ids)
	require.Equal(t, ids, listAll(t, svc, "edge", 1))

	resp, err := svc.ListVectors(context.Background(), &ListVectorsRequest{
		Namespace: "edge", Limit: 1, Cursor: strconv.FormatUint(math.MaxUint64, 10),
	})
	require.NoError(t, err)
	require.Len(t, resp.Vectors, 1)
	require.Equal(t, uint64(math.MaxUint64), resp.Vectors[0].ID)
	require.Empty(t, resp.NextCursor, "no ID follows MaxUint64")
}

func TestPerformIndexRebuild_PagesEveryVectorOnce(t *testing.T) {
	for name, ids := range map[string][]uint64{
		"pages":     listCursorIDs(),
		"maxUint64": {1, math.MaxUint64},
	} {
		t.Run(name, func(t *testing.T) {
			svc, _ := newListCursorService(t, "docs", ids)
			ns := scopedNamespace(tenantid.System, "docs")
			job := &RebuildJobState{JobID: "rebuild-" + name, Namespace: ns, InProgress: true, StartedAt: time.Now()}
			svc.jobMu.Lock()
			svc.rebuildJobs[job.JobID] = job
			svc.jobMu.Unlock()

			require.NoError(t, svc.performIndexRebuild(context.Background(), job.JobID, ns, &vector.NamespaceInfo{Name: ns, Dimension: 2}, nil))

			job.mu.RLock()
			defer job.mu.RUnlock()
			require.Equal(t, uint64(len(ids)), job.VectorsProcessed)
		})
	}
}
