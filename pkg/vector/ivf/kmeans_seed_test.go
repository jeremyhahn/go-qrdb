// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC

//go:build qrdb_vector

package ivf

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Config.Seed controls k-means initialisation, and that is asserted DIRECTLY
// rather than inferred from recall.
//
// TestIndex_SearchAccuracy runs several seeds, but it only asserts each one
// clears the quality floor. An implementation that ignored Seed, or kept using
// the package-global source, or seeded every KMeans differently, would clear
// that floor just as well — the recall test cannot tell a working seed from a
// missing one. The contract needs its own test or it is unverified whatever
// recall says (go-qrdb#92).
//
// Both initialisation methods are covered because both RNG paths changed:
// kmeans++ draws through Intn and Float32, random through Perm.
func TestKMeans_SeedControlsInitialisation(t *testing.T) {
	vectors := seededVectors(200, 16, 4242)

	for _, method := range []string{"kmeans++", "random"} {
		t.Run(method, func(t *testing.T) {
			cluster := func(seed int64) ([][]float32, []int) {
				t.Helper()
				km := NewKMeans(&Config{
					NumCentroids:   8,
					Dimensions:     16,
					MaxIterations:  25,
					Convergence:    0.0001,
					InitMethod:     method,
					DistanceMetric: L2Distance,
					Seed:           seed,
				})
				centroids, assignments, err := km.Cluster(vectors)
				require.NoError(t, err)
				return centroids, assignments
			}

			t.Run("the same seed reproduces the clustering", func(t *testing.T) {
				// Two INDEPENDENTLY constructed instances, so this tests the
				// seed rather than one object's internal state.
				centroidsA, assignA := cluster(7)
				centroidsB, assignB := cluster(7)

				assert.Equal(t, centroidsA, centroidsB,
					"the same seed produced different centroids: Seed does not "+
						"control initialisation, so clustering is not reproducible")
				assert.Equal(t, assignA, assignB,
					"the same seed produced different assignments")
			})

			t.Run("a different seed changes the clustering", func(t *testing.T) {
				// Without this, an implementation that ignored Seed entirely
				// and returned a constant clustering would pass the case above.
				centroidsA, _ := cluster(7)
				centroidsB, _ := cluster(99)

				assert.NotEqual(t, centroidsA, centroidsB,
					"two different seeds produced identical centroids: the seed "+
						"is not reaching the initialisation at all")
			})
		})
	}
}

// A zero Seed leaves initialisation nondeterministic, which is the production
// default.
//
// Asserted because it is the half of the contract that must NOT change: fixing
// the seed across a fleet would have every node make the same unlucky choice
// on the same data. The randomness is doing real work; Seed only makes it
// controllable.
func TestKMeans_ZeroSeedIsNondeterministic(t *testing.T) {
	vectors := seededVectors(200, 16, 4242)

	cluster := func() [][]float32 {
		km := NewKMeans(&Config{
			NumCentroids:   8,
			Dimensions:     16,
			MaxIterations:  25,
			Convergence:    0.0001,
			InitMethod:     "random",
			DistanceMetric: L2Distance,
			// Seed left at zero: seeded from the clock.
		})
		centroids, _, err := km.Cluster(vectors)
		require.NoError(t, err)
		return centroids
	}

	// Several attempts: two clock-seeded runs CAN coincide by chance, so a
	// single comparison would be flaky in the other direction — the assertion
	// is that they are not all identical.
	first := cluster()
	differs := false
	for i := 0; i < 5 && !differs; i++ {
		if !assert.ObjectsAreEqual(first, cluster()) {
			differs = true
		}
	}

	assert.True(t, differs,
		"six clock-seeded clusterings were identical: a zero Seed appears to be "+
			"deterministic, which would give every node in a fleet the same "+
			"initialisation on the same data")
}
