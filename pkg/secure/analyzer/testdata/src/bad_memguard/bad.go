package bad_memguard

import (
	"github.com/awnumar/memguard" // want `import of "github.com/awnumar/memguard" is forbidden outside pkg/secure.*`
)

func use() {
	_ = memguard.NewBuffer(32)
}
