// Copyright (c) 2026, go-compressions
// SPDX-License-Identifier: BSD-3-Clause

package compress

import (
	"bytes"
	"io"
	"runtime"
	"testing"
)

// allocated reports the bytes f allocated: TotalAlloc before and after, as the
// lzfse pilot measured.
func allocated(f func()) uint64 {
	var a, b runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&a)
	f()
	runtime.ReadMemStats(&b)
	return b.TotalAlloc - a.TotalAlloc
}

// lzwSquare builds a block-mode .Z stream of maxBits-bit codes in which every
// code after the first names the entry it is about to create (the "KwKwK" case):
// the literal 0, then 257, 258, 259 ... The string of code c is c-255 zero bytes,
// so the output grows as the square of the code count -- the worst expansion the
// format allows per code, and the stream any run of zeros makes.
//
// It packs bits as the reader unpacks them (least significant first, padded to a
// group of eight codes whenever the width grows), so a wrong output length means
// the packing and the reader disagree. compress(1) is the judge of the reader in
// compress_test.go; this is only the means of making a large stream without it.
func lzwSquare(maxBits uint) (stream []byte, out int) {
	var (
		acc    uint64
		nacc   uint
		pos    int64
		origin int64
	)
	body := []byte{}
	put := func(v uint32, n uint) {
		acc |= uint64(v) << nacc
		nacc += n
		pos += int64(n)
		for nacc >= 8 {
			body = append(body, byte(acc))
			acc >>= 8
			nacc -= 8
		}
	}
	width := uint(initBits)
	put(0, width)
	out = 1
	for c := uint32(clearCode + 1); c < 1<<maxBits; c++ {
		if width < maxBits && c > 1<<width-1 {
			k := int64(width) * 8
			rel := pos - origin
			for pad := (k - rel%k) % k; pad > 0; pad-- {
				put(0, 1)
			}
			origin = pos
			width++
		}
		put(c, width)
		out += int(c) - 255
	}
	if nacc > 0 {
		body = append(body, byte(acc))
	}
	return append([]byte{magic1, magic2, flagBlockMode | byte(maxBits)}, body...), out
}

// TestBombs_ExpansionIsPaidInTimeNotMemory: a 12-bit stream of 3 KB decodes to
// 7.4 MB and a 16-bit one of 123 KB to 2.1 GB -- the format has no ceiling to
// find -- but the reader holds one string at a time. Before it reused its output
// buffer every code allocated its string afresh: 71.5 MB of garbage for 64 MiB of
// zeros (measured on a compress(1) stream), now the tables and one string.
func TestBombs_ExpansionIsPaidInTimeNotMemory(t *testing.T) {
	stream, want := lzwSquare(12)
	if want < 1000*len(stream) {
		t.Fatalf("%d bytes -> %d: the stream does not expand like a bomb", len(stream), want)
	}
	var total int
	n := allocated(func() {
		r, err := NewReader(bytes.NewReader(stream))
		if err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 4096)
		for {
			k, err := r.Read(buf)
			for _, c := range buf[:k] {
				if c != 0 {
					t.Fatalf("byte %d is %#x, want 0", total, c)
				}
			}
			total += k
			if l := cap(r.(*reader).buf); l > 1<<13 {
				t.Fatalf("output buffer grew to %d bytes", l)
			}
			if err == io.EOF {
				return
			}
			if err != nil {
				t.Fatal(err)
			}
		}
	})
	if total != want {
		t.Errorf("decoded %d bytes, want %d", total, want)
	}
	if n >= 1<<20 {
		t.Errorf("decoding %d bytes allocated %d, want < 1 MiB", total, n)
	}
}

// TestBombs_WhatCompressOneMakesOfZeros: the same with the reference encoder's
// own stream (skipped where compress(1) is absent).
func TestBombs_WhatCompressOneMakesOfZeros(t *testing.T) {
	data := make([]byte, 16<<20)
	z := squeeze(t, data, 16)
	var total int64
	n := allocated(func() {
		r, err := NewReader(bytes.NewReader(z))
		if err != nil {
			t.Fatal(err)
		}
		total, err = io.Copy(io.Discard, r)
		if err != nil {
			t.Fatal(err)
		}
	})
	if total != int64(len(data)) {
		t.Errorf("decoded %d bytes, want %d", total, len(data))
	}
	if n >= 1<<20 {
		t.Errorf("decoding %d bytes (%d compressed) allocated %d, want < 1 MiB", total, len(z), n)
	}
}
