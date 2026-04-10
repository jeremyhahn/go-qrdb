//go:build !integration

package api

import (
	"errors"
	"testing"

	dberrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
)

func TestIVFConfigValidationError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &IVFConfigValidationError{Message: "nClusters must be > 0"}
		if got := e.Error(); got != "nClusters must be > 0" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewIVFConfigValidationError("ivf.init", "dimensions required")
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestIVFInvalidConfigError(t *testing.T) {
	cause := errors.New("bad config")
	t.Run("Error", func(t *testing.T) {
		e := &IVFInvalidConfigError{Cause: cause}
		want := "invalid config: bad config"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("Unwrap", func(t *testing.T) {
		e := &IVFInvalidConfigError{Cause: cause}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewIVFInvalidConfigError("ivf.init", cause)
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestIVFEmptyTrainingSetError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &IVFEmptyTrainingSetError{}
		if got := e.Error(); got != "cannot train on empty vector set" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewIVFEmptyTrainingSetError("ivf.train")
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestIVFDimensionMismatchError(t *testing.T) {
	t.Run("WithIndex", func(t *testing.T) {
		e := &IVFDimensionMismatchError{Index: 3, Got: 128, Expected: 256}
		want := "vector 3 has 128 dimensions, expected 256"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("WithoutIndex", func(t *testing.T) {
		e := &IVFDimensionMismatchError{Index: -1, Got: 128, Expected: 256}
		want := "vector has 128 dimensions, expected 256"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewIVFDimensionMismatchError("ivf.add", 0, 64, 128)
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestIVFNotTrainedError(t *testing.T) {
	t.Run("WithOperation", func(t *testing.T) {
		e := &IVFNotTrainedError{Operation: "querying"}
		want := "index must be trained before querying"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("WithoutOperation", func(t *testing.T) {
		e := &IVFNotTrainedError{}
		want := "index must be trained"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewIVFNotTrainedError("ivf.query", "searching")
		if de.Code != dberrors.ErrPreconditionFailed {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestIVFSimpleFieldErrors(t *testing.T) {
	tests := []struct {
		name    string
		errFn   func() error
		newFn   func() *dberrors.DragonError
		wantMsg string
	}{
		{"DuplicateID", func() error { return &IVFDuplicateIDError{ID: "v1"} }, func() *dberrors.DragonError { return NewIVFDuplicateIDError("ivf.add", "v1") }, "vector with ID v1 already exists"},
		{"VectorNotFound", func() error { return &IVFVectorNotFoundError{ID: "v2"} }, func() *dberrors.DragonError { return NewIVFVectorNotFoundError("ivf.get", "v2") }, "vector with ID v2 not found"},
		{"UntrainedSave", func() error { return &IVFUntrainedSaveError{} }, func() *dberrors.DragonError { return NewIVFUntrainedSaveError("ivf.save") }, "cannot save untrained index"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := tt.errFn()
			if got := e.Error(); got != tt.wantMsg {
				t.Errorf("Error() = %q, want %q", got, tt.wantMsg)
			}
			de := tt.newFn()
			if de.Err.Error() != tt.wantMsg {
				t.Errorf("constructor msg = %q, want %q", de.Err.Error(), tt.wantMsg)
			}
		})
	}
}

func TestIVFSaveError(t *testing.T) {
	cause := errors.New("disk full")
	t.Run("Error", func(t *testing.T) {
		e := &IVFSaveError{Action: "write index", Cause: cause}
		want := "failed to write index: disk full"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("Unwrap", func(t *testing.T) {
		e := &IVFSaveError{Action: "test", Cause: cause}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewIVFSaveError("ivf.persist", "serialize", cause)
		if de.Code != dberrors.ErrStorage {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestIVFQueryDimensionError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &IVFQueryDimensionError{Got: 64, Expected: 128}
		want := "query has 64 dimensions, expected 128"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewIVFQueryDimensionError("ivf.query", 32, 256)
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestKMeansErrors(t *testing.T) {
	cause := errors.New("math error")

	t.Run("EmptyVectorSet", func(t *testing.T) {
		e := &KMeansEmptyVectorSetError{}
		if got := e.Error(); got != "cannot cluster empty vector set" {
			t.Errorf("got %q", got)
		}
		de := NewKMeansEmptyVectorSetError("kmeans.cluster")
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})

	t.Run("InsufficientVectors", func(t *testing.T) {
		e := &KMeansInsufficientVectorsError{Clusters: 10, Vectors: 3}
		want := "cannot create 10 clusters from 3 vectors"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		de := NewKMeansInsufficientVectorsError("kmeans.cluster", 5, 2)
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})

	t.Run("InitError", func(t *testing.T) {
		e := &KMeansInitError{Cause: cause}
		want := "failed to initialize centroids: math error"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
		de := NewKMeansInitError("kmeans.init", cause)
		if de.Code != dberrors.ErrInternal {
			t.Errorf("code = %v", de.Code)
		}
	})

	t.Run("RecomputeError", func(t *testing.T) {
		e := &KMeansRecomputeError{Iteration: 5, Cause: cause}
		want := "failed to recompute centroids at iteration 5: math error"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
		de := NewKMeansRecomputeError("kmeans.iterate", 3, cause)
		if de.Code != dberrors.ErrInternal {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestHNSWPersistenceError(t *testing.T) {
	cause := errors.New("io error")
	t.Run("Error", func(t *testing.T) {
		e := &HNSWPersistenceError{Action: "save graph", Cause: cause}
		want := "failed to save graph: io error"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("Unwrap", func(t *testing.T) {
		e := &HNSWPersistenceError{Action: "load", Cause: cause}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewHNSWPersistenceError("hnsw.save", "serialize", cause)
		if de.Code != dberrors.ErrStorage {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestIVFClusteringFailedError(t *testing.T) {
	cause := errors.New("convergence failed")
	t.Run("Error", func(t *testing.T) {
		e := &IVFClusteringFailedError{Cause: cause}
		want := "k-means clustering failed: convergence failed"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("Unwrap", func(t *testing.T) {
		e := &IVFClusteringFailedError{Cause: cause}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewIVFClusteringFailedError("ivf.train", cause)
		if de.Code != dberrors.ErrInternal {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestRAGSearchError(t *testing.T) {
	cause := errors.New("embedding failed")
	t.Run("Error", func(t *testing.T) {
		e := &RAGSearchError{Action: "vector search", Cause: cause}
		want := "vector search failed: embedding failed"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("Unwrap", func(t *testing.T) {
		e := &RAGSearchError{Action: "search", Cause: cause}
		if e.Unwrap() != cause {
			t.Error("Unwrap failed")
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewRAGSearchError("rag.search", "similarity", cause)
		if de.Code != dberrors.ErrInternal {
			t.Errorf("code = %v", de.Code)
		}
	})
}
