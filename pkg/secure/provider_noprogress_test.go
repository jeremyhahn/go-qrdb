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
	"time"
)

// stalledReader returns (0, nil) for its first `stalls` reads, then reads r.
// A reader that never makes progress has stalls < 0.
type stalledReader struct {
	r      io.Reader
	stalls int
	reads  int
}

func (s *stalledReader) Read(p []byte) (int, error) {
	s.reads++
	if s.stalls < 0 || s.reads <= s.stalls {
		return 0, nil
	}
	return s.r.Read(p)
}

// stalledSeeker is a stalledReader that can also seek, so it takes the bulk
// path.
type stalledSeeker struct {
	stalledReader
	s io.Seeker
}

func (s *stalledSeeker) Seek(offset int64, whence int) (int64, error) {
	return s.s.Seek(offset, whence)
}

// noProgressBound is how long a read that must give up is allowed to take. A
// read that loops forever on (0, nil) would otherwise hang the suite; this
// turns that into a failure naming the defect.
const noProgressBound = 10 * time.Second

// passphraseWithin runs Passphrase on r and fails the test if it does not
// return within noProgressBound.
func passphraseWithin(t *testing.T, r io.Reader) (*Buffer, error) {
	t.Helper()
	type result struct {
		buf *Buffer
		err error
	}
	done := make(chan result, 1)
	go func() {
		buf, err := NewStdinProviderFromReader(r).Passphrase(context.Background(), "")
		done <- result{buf, err}
	}()
	select {
	case res := <-done:
		return res.buf, res.err
	case <-time.After(noProgressBound):
		t.Fatalf("Passphrase did not return: a reader stuck on (0, nil) must end in io.ErrNoProgress, not loop")
		return nil, nil
	}
}

// A stream that keeps answering (0, nil) is broken, as io.Reader's contract
// puts it, and both read paths give up the way bufio does: io.ErrNoProgress
// after maxConsecutiveEmptyReads empty reads in a row.
func TestStdinProvider_NoProgressEndsTheRead(t *testing.T) {
	paths := map[string]func() io.Reader{
		"seekable": func() io.Reader {
			sr := strings.NewReader("never-read\n")
			return &stalledSeeker{stalledReader: stalledReader{r: sr, stalls: -1}, s: sr}
		},
		"unseekable": func() io.Reader {
			return &stalledReader{r: strings.NewReader("never-read\n"), stalls: -1}
		},
	}
	for name, mk := range paths {
		t.Run(name, func(t *testing.T) {
			buf, err := passphraseWithin(t, mk())
			if err == nil {
				buf.Destroy()
				t.Fatalf("a stream that never makes progress produced a passphrase")
			}
			var pe *ProviderError
			if !errors.As(err, &pe) || !errors.Is(err, io.ErrNoProgress) {
				t.Fatalf("Passphrase = %v (%T), want *ProviderError wrapping io.ErrNoProgress", err, err)
			}
		})
	}
}

// A few empty reads followed by data are tolerated, as they are by bufio: the
// limit is on CONSECUTIVE empty reads, and a stream that recovers is read.
func TestStdinProvider_ToleratesFewerEmptyReadsThanTheLimit(t *testing.T) {
	paths := map[string]func() io.Reader{
		"seekable": func() io.Reader {
			sr := strings.NewReader("hunter2\nrest")
			return &stalledSeeker{stalledReader: stalledReader{r: sr, stalls: maxConsecutiveEmptyReads - 1}, s: sr}
		},
		"unseekable": func() io.Reader {
			return &stalledReader{r: strings.NewReader("hunter2\nrest"), stalls: maxConsecutiveEmptyReads - 1}
		},
	}
	for name, mk := range paths {
		t.Run(name, func(t *testing.T) {
			buf, err := passphraseWithin(t, mk())
			if err != nil {
				t.Fatalf("Passphrase: %v", err)
			}
			defer buf.Destroy()
			if string(contents(buf)) != "hunter2" {
				t.Fatalf("passphrase = %q, want %q", contents(buf), "hunter2")
			}
		})
	}
}
