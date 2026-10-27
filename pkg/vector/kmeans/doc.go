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

// Package kmeans holds k-means clustering logic shared by every ANN index
// family under pkg/vector that runs its own k-means or k-means++ (currently
// pkg/vector/ivf and pkg/vector/quantization), so it does not exist to be
// imported by anything outside pkg/vector. It imports nothing from this
// repository -- only the standard library -- so nothing importing it can
// create a cycle back here.
//
// # Empty-cluster repair
//
// [RepairEmptyClusters] moves the single farthest displaceable point into each
// empty cluster in turn. k-means++ initialisation can place two centroids on
// the same point when every remaining distance is zero, and the repair makes
// that duplicate transient rather than permanent, provided the training data
// contains enough distinct positions to fill every cluster.
//
// Both callers invoke it after their refinement loop, with the assignments and
// cluster sizes that loop left behind: ivf's KMeans passes the index's
// configured distance function (L2, Cosine or the signed DotProduct), raw and
// unsquared, and quantization's kmeansCluster passes its squared L2 distance.
// The distance callback therefore need not be a metric: the repair only
// compares two of its outputs with each other, and assumes no scale, sign or
// squaring.
//
// # Donor search
//
// The donor search considers every cluster with more than one member, not only
// the largest. A cluster can be large in count while every one of its points
// sits exactly on its centroid, so every distance in it is zero, while a
// smaller cluster elsewhere holds a point far from its own centroid.
//
// The running maximum is seeded from the first eligible candidate, not from
// zero. A signed distance function can make every real distance in a cluster
// negative, and a zero threshold would reject them all and report no donor.
//
// A vector identical to its own centroid is excluded from the search rather
// than rejected after it: moving it could only duplicate a value that already
// exists, and excluding it lets a displaceable vector elsewhere win even when
// the identical one scores as farthest under a signed metric. A cluster is
// therefore left exactly as it was found only when no eligible candidate
// remains: every splittable cluster consists of vectors identical to their
// own centroid, every candidate distance was NaN, or no cluster has more than
// one member.
//
// # Non-finite distances
//
// A NaN candidate distance is skipped. Cosine distance is NaN for large but
// finite input, and IEEE 754 makes every comparison with NaN false, so a NaN
// accepted as the running maximum would keep the first candidate scanned
// instead of the farthest one.
//
// An infinite candidate distance is clamped to ±[math.MaxFloat32], the bound
// the k-means++ initialisers clamp to. Clamping keeps the order of candidates
// and changes only ties, which the search resolves in favour of the earlier
// candidate; no infinity reaches a centroid.
package kmeans
