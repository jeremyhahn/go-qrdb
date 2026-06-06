// Package allowed_memguard is under the go-qrdb/pkg/secure prefix so
// direct memguard imports are permitted here; no diagnostics expected.
package allowed_memguard

import (
	"github.com/awnumar/memguard"
)

func use() {
	_ = memguard.NewBuffer(32)
}
