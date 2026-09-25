// Copyright (c) 2026, go-compressions
// SPDX-License-Identifier: BSD-3-Clause

// Package compress reads the .Z format of compress(1) in pure Go, with no cgo.
//
// # Why the standard library cannot
//
// Go has compress/lzw, and it says what it is for: "LZW as used by the GIF and
// PDF file formats, which means variable-width codes up to 12 bits". The .Z
// format needs three things it does not have:
//
//   - codes up to SIXTEEN bits;
//   - a clear code that resets the table mid-stream, which the writer sends when
//     compression stops paying;
//   - and the group alignment — compress(1) writes codes in groups of eight, so
//     a group of nine-bit codes is nine whole bytes, and when the width grows or
//     the table is cleared it PADS to the end of the current group before
//     changing anything.
//
// The last is the one that makes .Z hard. It is invisible in small files: a
// reader that ignores it decodes everything until the first widening and then
// produces plausible rubbish, which is worse than failing.
//
//	r, err := compress.NewReader(f)
//	if err != nil { return err }
//	io.Copy(dst, r)
//
// # There is no writer, on purpose
//
// Reading .Z opens archives that already exist, which is a real need. Writing one
// makes a file nobody should be making in 2026: gzip, xz and zstd all compress
// better and are read everywhere. The format is here to be understood, not
// perpetuated.
//
// # Verification, and what it cost
//
// Every fixture is made by the system's compress(1) and compared byte for byte.
// The corpus is built to reach the parts that matter: data that fills the table
// so the width grows, and data that defeats LZW so the writer clears — and the
// tests ASSERT that both happened, because a corpus that never widens tests
// nothing while looking thorough.
//
// ⛔ macOS's compress(1) writes streams its own -d refuses, at -b 9, -b 10 and
// -b 11. It exits 0 doing it. Three of these were used as fixtures at first and
// every failure they caused was reported against this reader, which was right to
// refuse them. So squeeze asks the reference to read back what it just wrote, and
// skips a width it cannot — the fixture is checked before the subject.
package compress
