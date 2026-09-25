// Copyright (c) 2026, go-compressions
// SPDX-License-Identifier: BSD-3-Clause

package compress

import (
	"errors"
	"fmt"
	"io"
)

// The three bytes a .Z stream begins with: two magic bytes and a flags byte.
const (
	magic1 = 0x1F
	magic2 = 0x9D

	flagBits      = 0x1F // the low five bits are the maximum code width
	flagBlockMode = 0x80 // the table may be cleared, and code 256 says so
)

// The code widths the format allows. Nine is where every stream starts; sixteen
// is the widest compress(1) will write and the widest its own reader accepts.
const (
	initBits = 9
	maxBits  = 16
)

// clearCode is the code that resets the table, in block mode. It sits where the
// first table entry would otherwise go, which is why the first free entry is 257
// in block mode and 256 without it.
const clearCode = 256

// ErrHeader is returned when the first three bytes are not a .Z header.
var ErrHeader = errors.New("compress: not a .Z stream")

// ErrCorrupt is returned when the code stream cannot be decoded.
//
// It is distinct from io.ErrUnexpectedEOF on purpose: a .Z stream ENDS with
// whatever bits fill the last byte, so running out mid-code is the normal end
// and not damage. Only a code that cannot exist yet is damage.
var ErrCorrupt = errors.New("compress: corrupt .Z stream")

// NewReader returns a reader over the .Z stream in r.
//
// The header is read immediately, so a stream that is not .Z is refused here
// rather than at the first Read.
func NewReader(r io.Reader) (io.Reader, error) {
	br := newBitReader(r)
	head := make([]byte, 3)
	if _, err := io.ReadFull(br.r, head); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, fmt.Errorf("%w: only %d bytes", ErrHeader, len(head))
		}
		return nil, err
	}
	if head[0] != magic1 || head[1] != magic2 {
		return nil, fmt.Errorf("%w: magic %#02x %#02x", ErrHeader, head[0], head[1])
	}
	width := uint(head[2] & flagBits)
	if width < initBits || width > maxBits {
		return nil, fmt.Errorf("%w: %d bits, outside %d..%d",
			ErrHeader, width, initBits, maxBits)
	}
	z := &reader{
		br:        br,
		maxWidth:  width,
		blockMode: head[2]&flagBlockMode != 0,
		prefix:    make([]uint16, 1<<width),
		suffix:    make([]byte, 1<<width),
		stack:     make([]byte, 1<<width),
		oldCode:   -1,
	}
	for i := range z.suffix[:256] {
		z.suffix[i] = byte(i)
	}
	z.reset()
	z.free = 256
	if z.blockMode {
		z.free = clearCode + 1
	}
	return z, nil
}

// reader decodes one .Z stream.
type reader struct {
	br        *bitReader
	maxWidth  uint
	blockMode bool

	width   uint   // the current code width
	maxCode uint32 // the widest code this width can hold before growing
	free    uint32 // the next table entry to fill
	origin  int64  // where the current group of eight codes began

	prefix []uint16
	suffix []byte
	stack  []byte

	oldCode int32
	finChar byte

	out []byte // decoded bytes not yet handed to the caller
	err error

	// clears counts the table resets seen, so a test can assert that a corpus
	// reaches the clear path at all rather than hoping it does.
	clears int
}

// reset puts the width back to nine, as the start of a stream and every clear
// do.
func (z *reader) reset() { z.setWidth(initBits) }

// setWidth settles the width and the entry count that will make it grow again.
//
// ⛔ The threshold is `1<<w - 1`, and I had `1<<w - 2` because I assumed
// compress(1)'s MAXCODE(n) macro was the usual (1<<n)-1. It is `1L << n`. One off
// here widens a bit too early, so a nine-bit code is read where a ten-bit one was
// written, and from there every code is nonsense -- reported as "code 959 is past
// the 958 entries defined", which blames the stream. READ the macro.
//
// At the widest width the threshold is 1<<maxWidth, which no entry count reaches,
// so the width stops growing rather than growing past what the codes can hold.
func (z *reader) setWidth(w uint) {
	z.width = w
	if w == z.maxWidth {
		z.maxCode = 1 << z.maxWidth
		return
	}
	z.maxCode = 1<<w - 1
}

func (z *reader) Read(p []byte) (int, error) {
	for len(z.out) == 0 {
		if z.err != nil {
			return 0, z.err
		}
		z.err = z.step()
	}
	n := copy(p, z.out)
	z.out = z.out[n:]
	return n, nil
}

// step decodes one code, which appends between one and several thousand bytes.
func (z *reader) step() error {
	// The table filled at this width, so the next code is one bit wider -- after
	// padding to the end of the current group of eight.
	if z.free > z.maxCode {
		pos, err := z.br.skipToGroup(z.origin, z.width)
		if err != nil {
			return err
		}
		z.origin = pos
		z.setWidth(z.width + 1)
	}

	code, err := z.br.read(z.width)
	if err != nil {
		return err
	}

	// The first code of a stream, and of every clear, is a literal.
	if z.oldCode < 0 {
		if code >= 256 {
			return fmt.Errorf("%w: first code is %d, not a literal", ErrCorrupt, code)
		}
		z.finChar = byte(code)
		z.oldCode = int32(code)
		z.out = append(z.out, z.finChar)
		return nil
	}

	if code == clearCode && z.blockMode {
		clear(z.prefix)
		z.free = clearCode
		pos, err := z.br.skipToGroup(z.origin, z.width)
		if err != nil {
			return err
		}
		z.origin = pos
		z.clears++
		z.reset()
		// ⛔ oldCode is NOT reset, and free goes back to 256 rather than 257.
		//
		// I wrote the opposite first, reasoning that the code after a clear must
		// be a literal. It need not: the table's first 256 entries are still the
		// literals, so a code decodes normally against them, and the FIRST entry
		// added after a clear lands in slot 256 -- the clear code's own slot,
		// which no longer needs to be reserved. That is why compress(1) sets
		// free_ent to FIRST-1 there and leaves oldcode alone, and why it only
		// shows up on data that clears at all: everything short passed.
		return nil
	}

	incode := code
	top := len(z.stack)

	// The one case where a code refers to the entry it is about to create: the
	// string is whatever the previous one was, plus its own first byte.
	if code >= z.free {
		if code > z.free {
			return fmt.Errorf("%w: code %d is past the %d entries defined",
				ErrCorrupt, code, z.free)
		}
		top--
		z.stack[top] = z.finChar
		code = uint32(z.oldCode)
	}

	for code >= 256 {
		if top == 0 {
			// A prefix chain longer than the table can hold means it loops, which
			// a well-formed stream cannot produce.
			return fmt.Errorf("%w: the prefix chain does not end", ErrCorrupt)
		}
		top--
		z.stack[top] = z.suffix[code]
		code = uint32(z.prefix[code])
	}
	z.finChar = z.suffix[code]
	top--
	z.stack[top] = z.finChar
	z.out = append(z.out, z.stack[top:]...)

	// The new entry is the previous string plus this one's first byte. Once the
	// table is full it simply stops growing: in block mode the writer sends a
	// clear when that stops paying, and without block mode the table stays as it
	// is for the rest of the stream.
	if z.free < 1<<z.maxWidth {
		z.prefix[z.free] = uint16(z.oldCode)
		z.suffix[z.free] = z.finChar
		z.free++
	}
	z.oldCode = int32(incode)
	return nil
}
