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

package secure

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// countingReader counts Read calls — each is one read(2) against a real
// stream — and passes them to r.
type countingReader struct {
	r     io.Reader
	reads int
}

func (c *countingReader) Read(p []byte) (int, error) {
	c.reads++
	return c.r.Read(p)
}

// countingSeeker is a countingReader whose stream can also seek, the way
// os.Stdin can when it is redirected from a regular file.
type countingSeeker struct {
	countingReader
	s io.Seeker
}

func (c *countingSeeker) Seek(offset int64, whence int) (int64, error) {
	return c.s.Seek(offset, whence)
}

func newCountingSeeker(in string) (*countingSeeker, *strings.Reader) {
	sr := strings.NewReader(in)
	return &countingSeeker{countingReader: countingReader{r: sr}, s: sr}, sr
}

// failingSeeker is a stream whose type claims io.Seeker but which cannot
// seek, which is what *os.File is for a pipe or a terminal (ESPIPE). After
// failAfter successful seeks every later one fails.
type failingSeeker struct {
	countingReader
	s         io.Seeker
	failAfter int
	seeks     int
	err       error
}

func (f *failingSeeker) Seek(offset int64, whence int) (int64, error) {
	f.seeks++
	if f.seeks > f.failAfter {
		return 0, f.err
	}
	return f.s.Seek(offset, whence)
}

// readerOnly hides every method of r but Read, so a strings.Reader stands in
// for a stream with no Seek at all.
type readerOnly struct{ r io.Reader }

func (o readerOnly) Read(p []byte) (int, error) { return o.r.Read(p) }

// TestStdinProvider_SeekableStreamReadsInBulk is go-qrdb#440's proof: on a
// stream that can seek, the line is read into the mlocked buffer in bulk and
// the bytes read past the newline are handed back to the stream by seeking,
// so the syscall count drops from one per byte to a handful WITHOUT
// over-reading. The remainder assertion is the half that forbids the easy
// version of this change.
func TestStdinProvider_SeekableStreamReadsInBulk(t *testing.T) {
	pass := strings.Repeat("p", 1000)
	cs, sr := newCountingSeeker(pass + "\nthe-rest-of-the-stream\n")
	buf, err := NewStdinProviderFromReader(cs).Passphrase(context.Background(), "")
	if err != nil {
		t.Fatalf("Passphrase: %v", err)
	}
	defer buf.Destroy()
	if string(contents(buf)) != pass {
		t.Fatalf("passphrase has %d bytes, want %d", len(contents(buf)), len(pass))
	}
	if cs.reads > 2 {
		t.Fatalf("a 1000-byte passphrase took %d reads; the bulk path takes at most 2", cs.reads)
	}
	rest, err := io.ReadAll(sr)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(rest) != "the-rest-of-the-stream\n" {
		t.Fatalf("remaining stream = %q; the bulk read must seek back past the newline", rest)
	}
}

// TestStdinProvider_UnseekableStreamStillDoesNotOverRead pins why the bulk
// path is only taken on a seekable stream. A pipe cannot give bytes back, so
// any read larger than one byte can consume past the newline and steal them
// from the stream's next reader; there the read stays byte-at-a-time.
func TestStdinProvider_UnseekableStreamStillDoesNotOverRead(t *testing.T) {
	sr := strings.NewReader("hunter2\nthe-rest-of-the-stream\n")
	cr := &countingReader{r: readerOnly{r: sr}}
	buf, err := NewStdinProviderFromReader(cr).Passphrase(context.Background(), "")
	if err != nil {
		t.Fatalf("Passphrase: %v", err)
	}
	defer buf.Destroy()
	if string(contents(buf)) != "hunter2" {
		t.Fatalf("passphrase = %q, want %q", contents(buf), "hunter2")
	}
	if cr.reads != len("hunter2\n") {
		t.Fatalf("unseekable stream took %d reads, want one per byte (%d)", cr.reads, len("hunter2\n"))
	}
	rest, err := io.ReadAll(sr)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(rest) != "the-rest-of-the-stream\n" {
		t.Fatalf("remaining stream = %q", rest)
	}
}

// TestStdinProvider_SeekerThatCannotSeekIsReadBytewise covers os.Stdin as a
// pipe: the type satisfies io.Seeker, the stream does not. The probe must
// send it down the byte-at-a-time path rather than over-read it.
func TestStdinProvider_SeekerThatCannotSeekIsReadBytewise(t *testing.T) {
	sr := strings.NewReader("hunter2\nrest")
	fs := &failingSeeker{countingReader: countingReader{r: sr}, s: sr, err: errors.New("illegal seek")}
	buf, err := NewStdinProviderFromReader(fs).Passphrase(context.Background(), "")
	if err != nil {
		t.Fatalf("Passphrase: %v", err)
	}
	defer buf.Destroy()
	if string(contents(buf)) != "hunter2" {
		t.Fatalf("passphrase = %q, want %q", contents(buf), "hunter2")
	}
	rest, err := io.ReadAll(sr)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(rest) != "rest" {
		t.Fatalf("remaining stream = %q; an unseekable stream must not be over-read", rest)
	}
}

// TestStdinProvider_FailedSeekBackIsAnError covers a stream that seeked for
// the probe and then refused to seek back. The bytes past the newline have
// been consumed and cannot be returned, so the stream's next reader would
// silently miss them: that is refused, not reported as success.
func TestStdinProvider_FailedSeekBackIsAnError(t *testing.T) {
	sr := strings.NewReader("hunter2\nrest")
	injected := errors.New("seek refused")
	fs := &failingSeeker{countingReader: countingReader{r: sr}, s: sr, failAfter: 1, err: injected}
	buf, err := NewStdinProviderFromReader(fs).Passphrase(context.Background(), "")
	if err == nil {
		buf.Destroy()
		t.Fatalf("a refused seek-back must fail the read")
	}
	var pe *ProviderError
	if !errors.As(err, &pe) || !errors.Is(err, injected) {
		t.Fatalf("Passphrase = %v (%T), want *ProviderError wrapping the seek error", err, err)
	}
}

// TestStdinProvider_BothPathsAgree runs the line-ending, length and error
// cases through the bulk (seekable) and byte-at-a-time (unseekable) paths and
// requires the same answer from each, including what is left on the stream.
func TestStdinProvider_BothPathsAgree(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		want  string
		rest  string
		isErr bool
	}{
		{name: "no terminator", in: "abc", want: "abc"},
		{name: "lf", in: "abc\n", want: "abc"},
		{name: "crlf", in: "abc\r\n", want: "abc"},
		{name: "lf only", in: "\n", isErr: true},
		{name: "crlf only", in: "\r\n", isErr: true},
		{name: "empty", in: "", isErr: true},
		{name: "bare cr at eof", in: "abc\r", want: "abc\r"},
		{name: "stops at first newline", in: "abc\ndef\n", want: "abc", rest: "def\n"},
		{name: "at max length", in: strings.Repeat("a", MaxStdinPassphraseLen) + "\nx", want: strings.Repeat("a", MaxStdinPassphraseLen), rest: "x"},
		{name: "one over max", in: strings.Repeat("a", MaxStdinPassphraseLen+1) + "\n", isErr: true},
		{name: "max without newline", in: strings.Repeat("a", MaxStdinPassphraseLen), want: strings.Repeat("a", MaxStdinPassphraseLen)},
	}
	paths := map[string]func(*strings.Reader) io.Reader{
		"seekable":   func(sr *strings.Reader) io.Reader { return sr },
		"unseekable": func(sr *strings.Reader) io.Reader { return readerOnly{r: sr} },
	}
	for pathName, wrap := range paths {
		for _, tc := range cases {
			t.Run(pathName+"/"+tc.name, func(t *testing.T) {
				sr := strings.NewReader(tc.in)
				buf, err := NewStdinProviderFromReader(wrap(sr)).Passphrase(context.Background(), "")
				if tc.isErr {
					if err == nil {
						buf.Destroy()
						t.Fatalf("expected an error")
					}
					var pe *ProviderError
					if !errors.As(err, &pe) {
						t.Fatalf("error %T, want *ProviderError", err)
					}
					return
				}
				if err != nil {
					t.Fatalf("Passphrase: %v", err)
				}
				defer buf.Destroy()
				if string(contents(buf)) != tc.want {
					t.Fatalf("passphrase = %q, want %q", contents(buf), tc.want)
				}
				rest, err := io.ReadAll(sr)
				if err != nil {
					t.Fatalf("ReadAll: %v", err)
				}
				if string(rest) != tc.rest {
					t.Fatalf("remaining stream = %q, want %q", rest, tc.rest)
				}
			})
		}
	}
}
