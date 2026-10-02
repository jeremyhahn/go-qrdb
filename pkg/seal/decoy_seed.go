// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package seal

import (
	"context"
	"crypto/rand"
	"io"
	"strconv"
)

// decoySeedPathSuffix names a domain's decoy seed beside its wrapped key.
const decoySeedPathSuffix = ".decoy-seed"

// decoySeedSize is the decoy seed's length in bytes.
const decoySeedSize = 32

// DecoySeedSizeError reports a persisted decoy seed of the wrong length.
type DecoySeedSizeError struct {
	// Size is the length found.
	Size int
}

// Error returns a human-readable description.
func (e *DecoySeedSizeError) Error() string {
	return "qrdb/seal: persisted decoy seed has the wrong length (" + strconv.Itoa(e.Size) + ")"
}

// DecoySeed returns domain's decoy seed, creating it on first use. The holder
// authority derives the decoy challenges it answers for names outside the
// roster from it, so each name's decoy is the same on every request, across
// restarts, exactly as a real holder's enrolled key is.
//
// It is stored with the domain's roster metadata, in the barrier's base
// storage beside the wrapped key, so it is readable while the barrier is
// sealed, which is when the quorum is reached. Like the roster, it is not
// encrypted: whoever reads the base storage reads the roster itself, and the
// seed tells them nothing the roster does not. Creation is a PutIfAbsent, so
// two processes racing to create it agree on one seed.
func (b *StorageBarrier) DecoySeed(ctx context.Context, domain QuorumDomain) ([]byte, error) {
	if !domain.Valid() {
		return nil, &QuorumOpInvalidError{Domain: domain}
	}
	path := b.config.RootKeyPath + "." + string(domain) + decoySeedPathSuffix
	// Some backends answer a missing key with an empty value rather than
	// a not-found error; both mean no seed has been created yet.
	seed, err := b.base.Get(ctx, path)
	if err == nil && len(seed) > 0 {
		return checkDecoySeed(seed)
	}
	if err != nil && !isSecretNotFound(err) {
		return nil, err
	}
	fresh := make([]byte, decoySeedSize)
	if _, err := io.ReadFull(rand.Reader, fresh); err != nil {
		return nil, err
	}
	stored, err := b.base.PutIfAbsent(ctx, path, fresh)
	if err != nil {
		return nil, err
	}
	if stored {
		return fresh, nil
	}
	seed, err = b.base.Get(ctx, path)
	if err != nil {
		return nil, err
	}
	return checkDecoySeed(seed)
}

// checkDecoySeed refuses a persisted seed of the wrong length.
func checkDecoySeed(seed []byte) ([]byte, error) {
	if len(seed) != decoySeedSize {
		return nil, &DecoySeedSizeError{Size: len(seed)}
	}
	return seed, nil
}
