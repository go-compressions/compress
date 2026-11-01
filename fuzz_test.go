// Copyright (c) 2026, go-compressions
// SPDX-License-Identifier: BSD-3-Clause

package compress

import (
	"bytes"
	"io"
	"testing"
)

// FuzzReader: no stream panics the reader, and what it holds stays within the
// tables and one string, whatever the codes say. Output is capped at 8 MiB so
// the fuzzer's squares of zeros end.
func FuzzReader(f *testing.F) {
	small, _ := lzwSquare(10)
	f.Add(small)
	f.Add([]byte{magic1, magic2, flagBlockMode | 9, 0x00, 0x01, 0x02})
	f.Add([]byte{magic1, magic2, 12, 0x61, 0x00})
	f.Add([]byte{magic1, magic2, flagBlockMode | 16, 0x00, 0x01, 0x00, 0x00, 0x02})
	f.Fuzz(func(t *testing.T, src []byte) {
		r, err := NewReader(bytes.NewReader(src))
		if err != nil {
			return
		}
		if _, err := io.Copy(io.Discard, io.LimitReader(r, 8<<20)); err != nil {
			return
		}
		if l := cap(r.(*reader).buf); l > 1<<17 {
			t.Fatalf("output buffer grew to %d bytes", l)
		}
	})
}
