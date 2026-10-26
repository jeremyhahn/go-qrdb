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

package vector

import (
	"github.com/jeremyhahn/go-qrdb/pkg/vector/flat"
	"github.com/jeremyhahn/go-qrdb/pkg/vector/gpu"
	"github.com/jeremyhahn/go-qrdb/pkg/vector/hnsw"
	"github.com/jeremyhahn/go-qrdb/pkg/vector/ivf"
)

// ---------------------------------------------------------------------------
// HNSW index types
// ---------------------------------------------------------------------------

// HNSWIndexImpl is the concrete HNSW index implementation.
type HNSWIndexImpl = hnsw.HNSWIndex

// HNSWIndexConfig configures HNSW index parameters.
type HNSWIndexConfig = hnsw.Config

// NewHNSWIndexImpl creates a new HNSW index.
var NewHNSWIndexImpl = hnsw.NewHNSWIndex

// HNSWDistanceMetric identifies the distance metric for HNSW indexes.
type HNSWDistanceMetric = hnsw.DistanceMetric

// HNSWDistanceMetric constants.
const (
	HNSWL2Distance         = hnsw.L2Distance
	HNSWCosineDistance     = hnsw.CosineDistance
	HNSWDotProductDistance = hnsw.DotProductDistance
	HNSWManhattanDistance  = hnsw.ManhattanDistance
	HNSWHammingDistance    = hnsw.HammingDistance
)

// ---------------------------------------------------------------------------
// Flat index types
// ---------------------------------------------------------------------------

// FlatIndex is a brute-force vector index.
type FlatIndex = flat.Index

// FlatSearchResult holds a single flat index search result.
type FlatSearchResult = flat.SearchResult

// NewFlatIndex creates a new flat (brute-force) vector index.
var NewFlatIndex = flat.NewIndex

// ---------------------------------------------------------------------------
// IVF index types
// ---------------------------------------------------------------------------

// IVFIndex is the interface for IVF (Inverted File) indexes.
type IVFIndex = ivf.Index

// IVFConfig configures IVF index parameters.
type IVFConfig = ivf.Config

// IVFResult holds a single IVF search result.
type IVFResult = ivf.Result

// NewIVFIndex creates a new IVF index.
var NewIVFIndex = ivf.NewIndex

// IVFDistanceMetric identifies the distance metric for IVF indexes.
type IVFDistanceMetric = ivf.DistanceMetric

// IVFDistanceMetric constants.
const (
	IVFL2Distance         = ivf.L2Distance
	IVFCosineDistance     = ivf.CosineDistance
	IVFDotProductDistance = ivf.DotProductDistance
)

// ---------------------------------------------------------------------------
// GPU index types
// ---------------------------------------------------------------------------

// ErrGPUNotCompiled is returned when GPU support is not compiled in.
var ErrGPUNotCompiled = gpu.ErrGPUNotCompiled
