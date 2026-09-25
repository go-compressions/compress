// Copyright (c) 2026, go-compressions
// SPDX-License-Identifier: BSD-3-Clause

package compress

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"testing"
)

// failAt reads n bytes and then refuses.
//
// A decoder holds bits in flight, so the read that fails is rarely the call the
// caller made. An error dropped there gives back a silently short file, which is
// the one failure nobody notices.
type failAt struct {
	r    io.Reader
	left int
	err  error
}

func (f *failAt) Read(p []byte) (int, error) {
	if f.left <= 0 {
		return 0, f.err
	}
	if len(p) > f.left {
		p = p[:f.left]
	}
	n, err := f.r.Read(p)
	f.left -= n
	if err != nil {
		return n, err
	}
	return n, nil
}

func TestAFailingReaderIsReported(t *testing.T) {
	boom := errors.New("the disk went away")
	z := squeeze(t, bytes.Repeat([]byte("the quick brown fox. "), 5000), 16)

	for _, after := range []int{0, 1, 2, 3, 4, 40, 400} {
		r, err := NewReader(&failAt{r: bytes.NewReader(z), left: after, err: boom})
		if after < 3 {
			// The header itself does not arrive.
			if err == nil {
				t.Errorf("after %d bytes: NewReader succeeded", after)
			}
			continue
		}
		if err != nil {
			t.Fatalf("after %d bytes: NewReader: %v", after, err)
		}
		if _, err := io.ReadAll(r); !errors.Is(err, boom) {
			t.Errorf("after %d bytes: reading = %v, want %v", after, err, boom)
		}
	}
}

// TestABufioReaderIsNotWrappedTwice. The decoder needs byte-at-a-time reads, so
// it buffers -- and buffering a buffer costs a copy of everything for nothing.
func TestABufioReaderIsNotWrappedTwice(t *testing.T) {
	z := squeeze(t, []byte("a short one"), 16)
	inner := bufio.NewReader(bytes.NewReader(z))
	r, err := NewReader(inner)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.(*reader).br.r; got != inner {
		t.Errorf("the bufio.Reader was wrapped again: %p != %p", got, inner)
	}
	if b, err := io.ReadAll(r); err != nil || string(b) != "a short one" {
		t.Errorf("read %q, %v", b, err)
	}
}

// TestAPrefixChainThatDoesNotEndIsRefused.
//
// ⛔ The format cannot produce this: an entry's prefix is always a code that
// already existed, so the chain strictly decreases and must reach a literal. The
// table is poisoned by hand to reach the guard.
//
// It is kept and tested rather than deleted because the alternative to a wrong
// byte here is a loop that never ends -- and a hang is worse than an error, twice
// over: it looks like a slow program, and a test suite reports it as a timeout
// rather than a failure.
func TestAPrefixChainThatDoesNotEndIsRefused(t *testing.T) {
	z := squeeze(t, bytes.Repeat([]byte("abcabcabc"), 50), 16)
	r, err := NewReader(bytes.NewReader(z))
	if err != nil {
		t.Fatal(err)
	}
	zr := r.(*reader)
	// Decode enough that the table holds entries, then make one point at itself.
	if _, err := io.ReadAll(io.LimitReader(zr, 20)); err != nil {
		t.Fatal(err)
	}
	for i := 256; i < 300; i++ {
		zr.prefix[i] = uint16(i)
		zr.suffix[i] = 'x'
	}
	zr.free = 300
	zr.out = nil
	zr.err = nil
	// Feed it a code that walks the poisoned chain.
	zr.oldCode = 260
	if err := func() error {
		for range 100 {
			if err := zr.step(); err != nil {
				return err
			}
		}
		return nil
	}(); !errors.Is(err, ErrCorrupt) {
		t.Errorf("walking a looping chain gave %v, want ErrCorrupt", err)
	}
}

// TestSkipToGroupStopsAtTheEndOfTheInput: a stream that ends exactly where an
// alignment would skip must end, not error about the skip.
func TestSkipToGroupStopsAtTheEndOfTheInput(t *testing.T) {
	b := newBitReader(bytes.NewReader([]byte{0xFF, 0xFF}))
	if _, err := b.read(9); err != nil {
		t.Fatal(err)
	}
	// Ask to skip to the next 9-byte group: there is not that much input.
	if _, err := b.skipToGroup(0, 9); !errors.Is(err, io.EOF) {
		t.Errorf("skipping past the end gave %v, want io.EOF", err)
	}
}

// TestSkipToGroupOnABoundarySkipsNothing: the arithmetic has to give zero when
// the position is already the origin, which is the case a formula copied from C
// gets wrong first.
func TestSkipToGroupOnABoundarySkipsNothing(t *testing.T) {
	data := bytes.Repeat([]byte{0xAB}, 40)
	b := newBitReader(bytes.NewReader(data))
	pos, err := b.skipToGroup(0, 9)
	if err != nil {
		t.Fatal(err)
	}
	if pos != 0 {
		t.Errorf("skipped to %d from the origin, want 0", pos)
	}
	// And one bit in, it goes to the end of the first group of nine bytes.
	if _, err := b.read(1); err != nil {
		t.Fatal(err)
	}
	pos, err = b.skipToGroup(0, 9)
	if err != nil {
		t.Fatal(err)
	}
	if pos != 72 {
		t.Errorf("from bit 1 it skipped to %d, want 72", pos)
	}
}

// TestAStreamThatEndsInsideAnAlignment.
//
// Both places that align can meet the end of the input there, and both must
// report it as the end rather than as damage: a .Z ends with whatever bits fill
// its last byte, so a file cut at a group boundary is indistinguishable from one
// that finished.
func TestAStreamThatEndsInsideAnAlignment(t *testing.T) {
	t.Run("after a clear", func(t *testing.T) {
		// Block mode, sixteen bits, then two nine-bit codes: a literal and CLEAR.
		// The alignment then wants to reach bit 72 and the file stops at 24.
		var b bytes.Buffer
		b.Write([]byte{0x1F, 0x9D, 0x90})
		var acc, n uint32
		put := func(v uint32) {
			acc |= v << n
			n += 9
			for n >= 8 {
				b.WriteByte(byte(acc))
				acc >>= 8
				n -= 8
			}
		}
		put('A')
		put(clearCode)
		if n > 0 {
			b.WriteByte(byte(acc))
		}

		r, err := NewReader(bytes.NewReader(b.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(r)
		if err != nil {
			t.Errorf("reading = %v, want the end of the file", err)
		}
		if string(got) != "A" {
			t.Errorf("got %q, want %q", got, "A")
		}
	})

	t.Run("before a widening", func(t *testing.T) {
		// Reaching this through the format needs 255 entries, which is 255 codes
		// of nine bits -- so the state is set by hand instead. What is being
		// tested is that the alignment's own end-of-input is the end of the file,
		// and the path there does not change that.
		r, err := NewReader(bytes.NewReader([]byte{0x1F, 0x9D, 0x90, 'A', 0}))
		if err != nil {
			t.Fatal(err)
		}
		zr := r.(*reader)
		if _, err := io.ReadAll(zr); err != nil {
			t.Fatal(err)
		}
		zr.err = nil
		zr.free = zr.maxCode + 1 // the table is full: the next code is wider
		if err := zr.step(); !errors.Is(err, io.EOF) {
			t.Errorf("step at the end of the input gave %v, want io.EOF", err)
		}
	})
}
