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
	"encoding/binary"
	"hash/fnv"
	"math"
)

// deterministicHash64 hashes namespace and salt together with FNV-1a, a
// fast, well-distributed, non-cryptographic hash. Nothing secret depends on
// this value -- it only decides which graph layer a node joins (HNSW) or
// which draw a k-means initialisation makes (IVF) -- so a CSPRNG would add
// cost without adding anything a reader of docs/vectors/engineering.md
// "Target: determinism" asked for: the same (namespace, salt) pair must hash
// identically on every replica, forever, which is a property of the
// algorithm, not of its unpredictability.
func deterministicHash64(namespace string, salt uint64) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(namespace)) // hash.Hash.Write never errors
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], salt)
	_, _ = h.Write(buf[:])
	return h.Sum64()
}

// DeterministicSeed derives a reproducible int64 seed from namespace and
// salt, for any index implementation whose construction takes a seed (IVF's
// Config.Seed). The same (namespace, salt) pair -- for IVF, salt is the
// namespace's training generation -- always produces the same seed on every
// replica, so a namespace retrained from the same records converges on the
// same centroids everywhere (docs/vectors/engineering.md "Target:
// determinism": "IVF k-means seeds from a hash of the namespace and its
// training generation").
func DeterministicSeed(namespace string, salt uint64) int64 {
	h := deterministicHash64(namespace, salt)
	// A seed of exactly 0 is indistinguishable from an unset Config.Seed,
	// which IVF treats as "seed from the clock" (see ivf.Config.Seed's doc
	// comment) -- the one value this function must never return.
	//nolint:gosec // G115: truncation to int64 is deliberate; only the bit pattern is used as a seed, never as a magnitude.
	seed := int64(h)
	if seed == 0 {
		seed = 1
	}
	return seed
}

// DeterministicUnit maps a hash of namespace and id onto the open interval
// (0, 1), exclusive of both endpoints: HNSW's layer formula takes -ln(u),
// which is undefined at u=0 and contributes nothing at u=1 (see
// DeterministicLayer).
func DeterministicUnit(namespace string, id uint64) float64 {
	h := deterministicHash64(namespace, id)
	// Keep the top 53 bits (float64's mantissa width) so every bit of
	// precision comes from the hash, then scale into (0, 1).
	const mantissaBits = 53
	frac := float64(h>>(64-mantissaBits)) / float64(uint64(1)<<mantissaBits)
	if frac <= 0 {
		frac = math.SmallestNonzeroFloat64
	}
	if frac >= 1 {
		frac = math.Nextafter(1, 0)
	}
	return frac
}

// DeterministicLayer computes the HNSW layer a node with id joins in
// namespace, using the same exponential-decay formula HNSW's own
// (previously clock- and fixed-seed-driven) layer selection used --
// floor(-ln(u) * ml) -- but with u derived purely from (namespace, id)
// instead of a shared *rand.Rand. Two replicas inserting the same ID into
// the same namespace therefore always place it at the same layer
// (docs/vectors/engineering.md "Target: determinism").
func DeterministicLayer(namespace string, id uint64, ml float64) int {
	u := DeterministicUnit(namespace, id)
	return int(math.Floor(-math.Log(u) * ml))
}
