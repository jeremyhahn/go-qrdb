package grpcweb

import (
	"encoding/base64"
	"encoding/binary"
	"io"
)

// frameHeaderLen is the length of a message's flag byte and big-endian length.
const frameHeaderLen = 5

// textReadSize is how much of a text-encoded request body textDecoder reads
// from the connection at a time.
const textReadSize = 4096

// textDecoder decodes an application/grpc-web-text request body. A text
// stream is a sequence of base64 chunks, each padded on its own
// (grpc-web-protocol.md, "Text-encoded (response) streams", 2), so padding may
// sit anywhere in the body, not only at its end. base64.NewDecoder stops at
// the first padding; textDecoder decodes every four-character quantum on its
// own, which reads a single chunk and a run of chunks alike. CR and LF are
// skipped, as base64.NewDecoder skips them.
type textDecoder struct {
	src     io.Reader
	in      []byte
	quantum [4]byte
	filled  int
	start   int64
	offset  int64
	buf     []byte
	out     []byte
	err     error
}

func newTextDecoder(src io.Reader) *textDecoder {
	return &textDecoder{src: src, in: make([]byte, textReadSize)}
}

// Read returns decoded bytes. A malformed quantum is a
// base64.CorruptInputError at the input offset where that quantum starts; a
// body ending inside a quantum is io.ErrUnexpectedEOF.
func (d *textDecoder) Read(p []byte) (int, error) {
	for len(d.out) == 0 {
		if d.err != nil {
			return 0, d.err
		}
		d.fill()
	}
	n := copy(p, d.out)
	d.out = d.out[n:]
	return n, nil
}

// fill reads once from the source and decodes every quantum it completes.
func (d *textDecoder) fill() {
	n, err := d.src.Read(d.in)
	out := d.buf[:0]
	defer func() { d.buf, d.out = out, out }()
	for _, c := range d.in[:n] {
		d.offset++
		if c == '\r' || c == '\n' {
			continue
		}
		if d.filled == 0 {
			d.start = d.offset - 1
		}
		d.quantum[d.filled] = c
		d.filled++
		if d.filled < len(d.quantum) {
			continue
		}
		d.filled = 0
		var decoded [3]byte
		m, derr := base64.StdEncoding.Decode(decoded[:], d.quantum[:])
		if derr != nil {
			d.err = base64.CorruptInputError(d.start)
			return
		}
		out = append(out, decoded[:m]...)
	}
	if err == io.EOF && d.filled != 0 {
		err = io.ErrUnexpectedEOF
	}
	d.err = err
}

// completeFrames returns the length of the longest prefix of b made of whole
// length-prefixed messages (grpc-web-protocol.md, "Message framing").
func completeFrames(b []byte) int {
	n := 0
	for len(b)-n >= frameHeaderLen {
		end := n + frameHeaderLen + int(binary.BigEndian.Uint32(b[n+1:n+frameHeaderLen]))
		if end > len(b) || end < n {
			break
		}
		n = end
	}
	return n
}
