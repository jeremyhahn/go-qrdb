package unborrowed_bytes

import "github.com/jeremyhahn/go-qrdb/pkg/secure"

// A test asserting a buffer's contents is out of rule 4's scope.
func assertContents(buf *secure.Buffer) bool {
	return string(buf.Bytes()) == "expected"
}
