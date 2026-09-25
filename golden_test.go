// Copyright (c) 2026, go-compressions
// SPDX-License-Identifier: BSD-3-Clause

package compress

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// golden says what each committed .Z file holds.
//
// The files were made HERE by compress(1), and each was checked to round-trip
// through its own -d before being committed -- because this machine's
// compress(1) writes streams it cannot read at three of its eight widths. What is
// stored is the compressed side only; the plaintext is generated, so the testdata
// stays small and there is nothing to keep in step by hand.
var golden = map[string]func() []byte{
	"onebyte":  func() []byte { return []byte("x") },
	"short":    func() []byte { return []byte("the quick brown fox") },
	"phrase":   func() []byte { return bytes.Repeat([]byte("the quick brown fox jumps. "), 7500) },
	"alphabet": func() []byte { return bytes.Repeat([]byte("abcdefghijklmnopqrstuvwxyz"), 4000) },
	"all256":   func() []byte { return bytes.Repeat(allBytes(), 400) },
	// mixed is text then noise: the text fills the table and the noise makes the
	// ratio collapse, which is the only thing that provokes a CLEAR. Nothing else
	// in this corpus reaches that path -- measured, not hoped.
	"mixed": func() []byte {
		return append(bytes.Repeat([]byte("the quick brown fox jumps over the lazy dog. "), 20000/45), noise(20000)...)
	},
}

// TestGoldenStreamsReadBack is the corpus that runs EVERYWHERE.
//
// The live compress(1) tests are the stronger judge and they skip on a machine
// without it -- which on this project's CI is three lanes out of eight, and took
// the coverage gate down with them. Committed streams make the suite self
// contained and leave the binary as an extra check rather than the only one.
func TestGoldenStreamsReadBack(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "*.Z"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 6 {
		t.Fatalf("only %d golden streams: the corpus is meant to cover several "+
			"widths and both the widening and the clear", len(files))
	}
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			name, _, ok := strings.Cut(strings.TrimSuffix(filepath.Base(path), ".Z"), "-b")
			if !ok {
				t.Fatalf("%s is not named <case>-b<bits>.Z", path)
			}
			want, ok := golden[name]
			if !ok {
				t.Fatalf("%s has no entry in golden, so nothing knows what it holds", name)
			}
			z, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			r, err := NewReader(bytes.NewReader(z))
			if err != nil {
				t.Fatalf("NewReader: %v", err)
			}
			got, err := io.ReadAll(r)
			if err != nil {
				t.Fatalf("reading: %v", err)
			}
			if expect := want(); !bytes.Equal(got, expect) {
				t.Errorf("read back %d bytes, want %d (first difference at %d)",
					len(got), len(expect), firstDiff(got, expect))
			}
		})
	}
}

// TestTheGoldenCorpusReachesWhatMatters asserts the PREMISE of the corpus, on the
// committed files, so it holds on every machine.
//
// A corpus that never widens and never clears tests nothing while looking
// thorough: the group alignment is the one hard thing in this format and it only
// runs at those two moments.
func TestTheGoldenCorpusReachesWhatMatters(t *testing.T) {
	var widened, cleared bool
	files, _ := filepath.Glob(filepath.Join("testdata", "*.Z"))
	for _, path := range files {
		z, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		r, err := NewReader(bytes.NewReader(z))
		if err != nil {
			t.Fatal(err)
		}
		zr := r.(*reader)
		before := zr.free
		if _, err := io.ReadAll(zr); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if zr.width > initBits {
			widened = true
		}
		// free going DOWN over the life of the stream can only be a clear.
		if zr.free < before || zr.clears > 0 {
			cleared = true
		}
		t.Logf("  %-16s ended at %2d bits, %5d entries, %d clears",
			filepath.Base(path), zr.width, zr.free, zr.clears)
	}
	if !widened {
		t.Error("no golden stream ever grew past nine bits: the alignment is untested")
	}
	if !cleared {
		t.Error("no golden stream ever cleared its table: half the alignment is untested")
	}
}
