package copyout_cases

import "github.com/jeremyhahn/go-qrdb/pkg/secure"

// A fixture copying protected bytes out for an assertion is out of scope.
func snapshot(buf *secure.Buffer) []byte {
	var out []byte
	_ = buf.WithBytes(func(b []byte) error {
		out = append([]byte(nil), b...)
		return nil
	})
	use(out)
	return nil
}
