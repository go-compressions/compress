// Copyright (c) 2026, go-compressions
// SPDX-License-Identifier: BSD-3-Clause

package compress

import (
	"bufio"
	"errors"
	"io"
)

// bitReader reads codes LEAST significant bit first.
//
// That is what compress(1) writes: its own reader takes three bytes
// little-endian, shifts right by the bit offset within the first, and masks. So
// a code straddling a byte boundary has its low bits in the EARLIER byte, which
// is the opposite of the order most formats with variable-width codes use --
// including the LZW of GIF, which is why compress/lzw's MSB mode does not help.
type bitReader struct {
	r   *bufio.Reader
	acc uint32 // pending bits, low end first
	n   uint   // how many bits are pending
	pos int64  // bits consumed since the header, for the group alignment
	err error
}

func newBitReader(r io.Reader) *bitReader {
	if br, ok := r.(*bufio.Reader); ok {
		return &bitReader{r: br}
	}
	return &bitReader{r: bufio.NewReader(r)}
}

// read returns the next n bits, or io.EOF if fewer than n remain.
//
// Fewer than n remaining is the NORMAL end of a .Z stream rather than damage:
// the last code is followed by however many bits fill the final byte, and
// compress(1)'s own reader stops for exactly this reason. A decoder that treats
// it as a short read reports every well-formed file as truncated.
func (b *bitReader) read(n uint) (uint32, error) {
	for b.n < n {
		c, err := b.r.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return 0, io.EOF
			}
			b.err = err
			return 0, err
		}
		b.acc |= uint32(c) << b.n
		b.n += 8
	}
	v := b.acc & (1<<n - 1)
	b.acc >>= n
	b.n -= n
	b.pos += int64(n)
	return v, nil
}

// skipToGroup discards bits up to the next boundary of a group of eight codes,
// measured from origin.
//
// ⛔ This is the rule naive readers miss, and it is invisible in small files.
// compress(1) writes codes in groups of eight, so a group of nine-bit codes is
// nine whole bytes; when the width grows or the table is cleared it PADS to the
// end of the current group before changing anything. A reader that just carries
// on is a few bits out from there onwards, and produces plausible rubbish
// instead of failing.
//
// The origin moves to each such boundary, so groups are measured from the last
// width change rather than from the start of the stream.
func (b *bitReader) skipToGroup(origin int64, nBits uint) (int64, error) {
	k := int64(nBits) * 8
	rel := b.pos - origin
	// The same arithmetic compress(1) does: round rel-1 up to the next multiple
	// of k, which for rel == 0 gives 0 and skips nothing.
	x := rel - 1
	target := x + (k - (x+k)%k)
	for rel < target {
		want := uint(min(target-rel, 8))
		if _, err := b.read(want); err != nil {
			return b.pos, err
		}
		rel += int64(want)
	}
	return b.pos, nil
}
