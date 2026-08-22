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

package ivf

import (
	"math"
	"math/rand"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/api"
)

// KMeans implements the k-means clustering algorithm.
type KMeans struct {
	config       *Config
	distanceFunc DistanceFunc

	// rng supplies the randomness for centroid initialisation.
	//
	// A field rather than the package-global source so a caller can make
	// clustering REPRODUCIBLE. Production leaves Config.Seed at zero and gets
	// a source seeded from the clock, which is the right default: fixed
	// initialisation across every index in a fleet would give every node the
	// same unlucky clustering on the same data.
	//
	// A test that measures clustering QUALITY needs the opposite. Recall is a
	// property of the centroid set, and Train produces ONE set shared by every
	// query — so an unlucky initialisation moves every query's result
	// together, and averaging over queries does not reduce that variance at
	// all. Only fixing the initialisation does (go-qrdb#92).
	rng *rand.Rand
}

// NewKMeans creates a new k-means clusterer.
func NewKMeans(config *Config) *KMeans {
	seed := config.Seed
	if seed == 0 {
		seed = time.Now().UnixNano()
	}
	return &KMeans{
		config:       config,
		distanceFunc: GetDistanceFunc(config.DistanceMetric),
		rng:          rand.New(rand.NewSource(seed)), //nolint:gosec // G404: k-means initialisation is not cryptographic
	}
}

// Cluster performs k-means clustering on the input vectors.
// Returns the centroids and cluster assignments.
func (km *KMeans) Cluster(vectors [][]float32) ([][]float32, []int, error) {
	if len(vectors) == 0 {
		return nil, nil, api.NewKMeansEmptyVectorSetError("kmeans.Cluster")
	}

	if len(vectors) < km.config.NumCentroids {
		return nil, nil, api.NewKMeansInsufficientVectorsError("kmeans.Cluster", km.config.NumCentroids, len(vectors))
	}

	// Initialize centroids
	centroids, err := km.initializeCentroids(vectors)
	if err != nil {
		return nil, nil, api.NewKMeansInitError("kmeans.Cluster", err)
	}

	// Track cluster assignments
	assignments := make([]int, len(vectors))
	clusterSizes := make([]int, km.config.NumCentroids)

	// Iterative refinement
	for iter := 0; iter < km.config.MaxIterations; iter++ {
		// Assignment step: assign each vector to nearest centroid
		changed := false
		for i := 0; i < km.config.NumCentroids; i++ {
			clusterSizes[i] = 0
		}

		for i, vec := range vectors {
			nearest := km.findNearestCentroid(vec, centroids)
			if nearest != assignments[i] {
				changed = true
				assignments[i] = nearest
			}
			clusterSizes[nearest]++
		}

		// Check for convergence
		if !changed {
			break
		}

		// Update step: recompute centroids
		newCentroids, err := km.recomputeCentroids(vectors, assignments, clusterSizes)
		if err != nil {
			return nil, nil, api.NewKMeansRecomputeError("kmeans.Cluster", iter, err)
		}

		// Check for convergence by centroid movement
		maxMovement := km.maxCentroidMovement(centroids, newCentroids)
		centroids = newCentroids

		if maxMovement < km.config.Convergence {
			break
		}
	}

	// Handle empty clusters by redistributing
	centroids = km.handleEmptyClusters(vectors, assignments, clusterSizes, centroids)

	return centroids, assignments, nil
}

// initializeCentroids initializes cluster centroids using k-means++ or random selection.
func (km *KMeans) initializeCentroids(vectors [][]float32) ([][]float32, error) {
	if km.config.InitMethod == "kmeans++" {
		return km.initializeKMeansPlusPlus(vectors)
	}
	return km.initializeRandom(vectors)
}

// initializeKMeansPlusPlus implements k-means++ initialization for better initial centroids.
func (km *KMeans) initializeKMeansPlusPlus(vectors [][]float32) ([][]float32, error) {
	centroids := make([][]float32, 0, km.config.NumCentroids)

	// Choose first centroid randomly
	firstIdx := km.rng.Intn(len(vectors))
	centroids = append(centroids, copyVector(vectors[firstIdx]))

	// Choose remaining centroids with probability proportional to D(x)^2
	distances := make([]float32, len(vectors))

	for k := 1; k < km.config.NumCentroids; k++ {
		// Compute distance to nearest centroid for each vector
		var totalWeight float32
		for i, vec := range vectors {
			minDist := float32(math.MaxFloat32)
			for _, centroid := range centroids {
				dist := km.distanceFunc(vec, centroid)
				if dist < minDist {
					minDist = dist
				}
			}
			distances[i] = minDist * minDist // D(x)^2
			totalWeight += distances[i]
		}

		// Choose next centroid with weighted probability
		if totalWeight == 0 {
			// All remaining points are duplicates, choose randomly
			for i := 0; i < len(vectors); i++ {
				alreadyChosen := false
				for _, c := range centroids {
					if km.distanceFunc(vectors[i], c) == 0 {
						alreadyChosen = true
						break
					}
				}
				if !alreadyChosen {
					centroids = append(centroids, copyVector(vectors[i]))
					break
				}
			}
			continue
		}

		target := km.rng.Float32() * totalWeight
		cumulative := float32(0)
		for i, weight := range distances {
			cumulative += weight
			if cumulative >= target {
				centroids = append(centroids, copyVector(vectors[i]))
				break
			}
		}
	}

	return centroids, nil
}

// initializeRandom randomly selects initial centroids from the dataset.
func (km *KMeans) initializeRandom(vectors [][]float32) ([][]float32, error) {
	centroids := make([][]float32, km.config.NumCentroids)
	indices := km.rng.Perm(len(vectors))

	for i := 0; i < km.config.NumCentroids; i++ {
		centroids[i] = copyVector(vectors[indices[i]])
	}

	return centroids, nil
}

// findNearestCentroid finds the index of the nearest centroid to the given vector.
func (km *KMeans) findNearestCentroid(vec []float32, centroids [][]float32) int {
	minDist := float32(math.MaxFloat32)
	nearest := 0

	for i, centroid := range centroids {
		dist := km.distanceFunc(vec, centroid)
		if dist < minDist {
			minDist = dist
			nearest = i
		}
	}

	return nearest
}

// recomputeCentroids calculates new centroids as the mean of assigned vectors.
func (km *KMeans) recomputeCentroids(vectors [][]float32, assignments []int, clusterSizes []int) ([][]float32, error) {
	dims := km.config.Dimensions
	centroids := make([][]float32, km.config.NumCentroids)

	// Initialize centroids
	for i := range centroids {
		centroids[i] = make([]float32, dims)
	}

	// Sum up vectors in each cluster
	for i, vec := range vectors {
		cluster := assignments[i]
		for d := 0; d < dims; d++ {
			centroids[cluster][d] += vec[d]
		}
	}

	// Compute means
	for i := range centroids {
		if clusterSizes[i] > 0 {
			for d := 0; d < dims; d++ {
				centroids[i][d] /= float32(clusterSizes[i])
			}
		}
	}

	return centroids, nil
}

// maxCentroidMovement computes the maximum distance any centroid has moved.
func (km *KMeans) maxCentroidMovement(old, new [][]float32) float32 {
	maxDist := float32(0)

	for i := range old {
		dist := km.distanceFunc(old[i], new[i])
		if dist > maxDist {
			maxDist = dist
		}
	}

	return maxDist
}

// handleEmptyClusters fixes empty clusters by splitting large clusters.
func (km *KMeans) handleEmptyClusters(vectors [][]float32, assignments []int, clusterSizes []int, centroids [][]float32) [][]float32 {
	// Find empty and large clusters
	for i, size := range clusterSizes {
		if size == 0 {
			// Find largest cluster
			maxSize := 0
			maxIdx := 0
			for j, s := range clusterSizes {
				if s > maxSize {
					maxSize = s
					maxIdx = j
				}
			}

			if maxSize > 1 {
				// Find farthest point in largest cluster
				maxDist := float32(0)
				farthestIdx := -1
				for j, vec := range vectors {
					if assignments[j] == maxIdx {
						dist := km.distanceFunc(vec, centroids[maxIdx])
						if dist > maxDist {
							maxDist = dist
							farthestIdx = j
						}
					}
				}

				if farthestIdx >= 0 {
					// Move farthest point to empty cluster
					centroids[i] = copyVector(vectors[farthestIdx])
					assignments[farthestIdx] = i
					clusterSizes[i] = 1
					clusterSizes[maxIdx]--
				}
			}
		}
	}

	return centroids
}

// copyVector creates a deep copy of a vector.
func copyVector(vec []float32) []float32 {
	copied := make([]float32, len(vec))
	copy(copied, vec)
	return copied
}

// FindNearestCentroids finds the k nearest centroids to the query vector.
func FindNearestCentroids(query []float32, centroids [][]float32, k int, distFunc DistanceFunc) []int {
	if k > len(centroids) {
		k = len(centroids)
	}

	// Compute distances to all centroids
	type centroidDist struct {
		idx  int
		dist float32
	}

	distances := make([]centroidDist, len(centroids))
	for i, centroid := range centroids {
		distances[i] = centroidDist{
			idx:  i,
			dist: distFunc(query, centroid),
		}
	}

	// Partial sort to find k nearest (more efficient than full sort)
	for i := 0; i < k; i++ {
		minIdx := i
		for j := i + 1; j < len(distances); j++ {
			if distances[j].dist < distances[minIdx].dist {
				minIdx = j
			}
		}
		if minIdx != i {
			distances[i], distances[minIdx] = distances[minIdx], distances[i]
		}
	}

	// Extract indices
	result := make([]int, k)
	for i := 0; i < k; i++ {
		result[i] = distances[i].idx
	}

	return result
}
