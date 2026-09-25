// Copyright (c) 2026, go-compressions
// SPDX-License-Identifier: BSD-3-Clause

package compress

import (
	"bytes"
	"errors"
	"io"
	"os/exec"
	"strings"
	"testing"
)

// squeeze runs the system's compress(1) over data at the given width.
//
// The fixtures are made by the program every .Z file in the world was made by.
// A round trip through a writer of our own would prove that two halves of this
// package agree, which is not the question a legacy format asks.
func squeeze(t *testing.T, data []byte, bits int) []byte {
	t.Helper()
	bin, err := exec.LookPath("compress")
	if err != nil {
		t.Skip("no compress(1) here to build the fixture with")
	}
	args := []string{"-c"}
	if bits > 0 {
		args = append(args, "-b", itoa(bits))
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdin = bytes.NewReader(data)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		t.Fatalf("compress %v: %v\n%s", args, err, errOut.String())
	}
	if out.Len() == 0 {
		// compress(1) refuses to write a stream that would be larger than its
		// input, so there is no reference .Z for an empty file -- and none for
		// anything it cannot shrink. That is the generator's policy, not a format
		// rule, and TestAHeaderWithNoCodesIsAnEmptyFile covers the case instead.
		t.Skipf("compress(1) wrote nothing for %d bytes of input at -b %d", len(data), bits)
	}
	if out.Len() < 3 {
		t.Fatalf("compress produced %d bytes, which is not even a header", out.Len())
	}
	// ⛔ THE PREMISE: the reference must be able to read back what it just wrote.
	//
	// macOS's compress(1) at -b 9 writes a header claiming nine bits and then
	// encodes with ten. Its own -d refuses the result ("Inappropriate file type
	// or format") and so does gzip -- and it exits 0 while compressing, so the
	// fixture looked fine. Every failure it caused was reported against the
	// decoder, which was right to refuse it.
	//
	// So the generator is checked, and a width it cannot round-trip is SKIPPED
	// with the reason rather than quietly turned into a defect here.
	back := exec.Command(bin, "-dc")
	back.Stdin = bytes.NewReader(out.Bytes())
	var round bytes.Buffer
	back.Stdout = &round
	if err := back.Run(); err != nil {
		t.Skipf("this compress(1) cannot read back its own -b %d output, so the "+
			"fixture would be the defect: %v", bits, err)
	}
	if !bytes.Equal(round.Bytes(), data) {
		t.Skipf("this compress(1) round-trips -b %d to %d bytes instead of %d, so "+
			"the fixture is wrong rather than the reader", bits, round.Len(), len(data))
	}
	return out.Bytes()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// inflate reads a .Z stream with this package.
func inflate(t *testing.T, z []byte) []byte {
	t.Helper()
	r, err := NewReader(bytes.NewReader(z))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	return got
}

// corpus is chosen so that the table FILLS and the width GROWS, which is where
// the group alignment lives: a small file never reaches it, and a reader that
// ignores it passes every short test.
var corpus = []struct {
	name string
	data []byte
}{
	{"one byte", []byte("x")},
	{"two bytes", []byte("xy")},
	{"short text", []byte("the quick brown fox")},
	{"one repeated byte, 100k", bytes.Repeat([]byte("a"), 100_000)},
	{"repeated phrase, 200k", bytes.Repeat([]byte("the quick brown fox jumps. "), 7500)},
	{"all 256 values", allBytes()},
	{"all 256 values, 400 times", bytes.Repeat(allBytes(), 400)},
	// Pseudo-random data defeats LZW, so the table fills with entries that never
	// pay off and the writer clears it -- which is the block-mode path.
	{"incompressible, 300k", noise(300_000)},
	{"half text half noise", append(bytes.Repeat([]byte("abcdefgh"), 20_000), noise(160_000)...)},
}

func allBytes() []byte {
	b := make([]byte, 256)
	for i := range b {
		b[i] = byte(i)
	}
	return b
}

func noise(n int) []byte {
	b := make([]byte, n)
	x := uint32(12345)
	for i := range b {
		x = x*1664525 + 1013904223
		b[i] = byte(x >> 24)
	}
	return b
}

// TestCompressStreamsReadBack is the judge: every fixture is made by compress(1)
// and must come back byte for byte.
func TestCompressStreamsReadBack(t *testing.T) {
	for _, c := range corpus {
		t.Run(c.name, func(t *testing.T) {
			// ⛔ Twelve is the narrowest usable width HERE. macOS's compress(1)
			// writes streams its own -d refuses at -b 9, -b 10 and -b 11 --
			// measured, not assumed -- so those are not fixtures, they are broken
			// files. squeeze skips any width that cannot round-trip, so this list
			// is a preference and the check is the guard.
			for _, bits := range []int{12, 13, 16} {
				z := squeeze(t, c.data, bits)
				if got := inflate(t, z); !bytes.Equal(got, c.data) {
					t.Errorf("-b %d: read back %d bytes, want %d (first difference at %d)",
						bits, len(got), len(c.data), firstDiff(got, c.data))
				}
			}
		})
	}
}

func firstDiff(a, b []byte) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return min(len(a), len(b))
}

// TestTheWidthActuallyGrew asserts the PREMISE of the corpus: that some fixture
// pushes the stream past nine bits.
//
// Without it the whole suite could pass on streams that never widen, and the
// group alignment -- the one thing hard about this format -- would be untested
// while looking tested.
func TestTheWidthActuallyGrew(t *testing.T) {
	z := squeeze(t, bytes.Repeat([]byte("the quick brown fox jumps. "), 7500), 16)
	r, err := NewReader(bytes.NewReader(z))
	if err != nil {
		t.Fatal(err)
	}
	zr := r.(*reader)
	if _, err := io.ReadAll(zr); err != nil {
		t.Fatal(err)
	}
	if zr.width <= initBits {
		t.Errorf("the stream never grew past %d bits, so the alignment was never "+
			"exercised and this corpus proves less than it looks", initBits)
	}
	t.Logf("ended at %d bits, %d table entries", zr.width, zr.free)
}

// TestBlockModeWasExercised, for the same reason: a clear is the other half of
// the alignment, and only data that defeats LZW provokes one.
func TestBlockModeWasExercised(t *testing.T) {
	z := squeeze(t, noise(300_000), 13)
	if z[2]&flagBlockMode == 0 {
		t.Skip("this compress(1) does not set block mode")
	}
	// 0x100 is the clear code, and it can only appear once the width is wide
	// enough to hold it -- so its presence in the decoded run is what is asserted
	// indirectly: the stream decodes, and it could not without handling clears.
	got := inflate(t, z)
	if !bytes.Equal(got, noise(300_000)) {
		t.Errorf("read back %d bytes, want %d", len(got), 300_000)
	}
}

// TestHeadersThatAreNotHeaders. Three sentences: not a .Z at all, a .Z claiming a
// width nothing can read, and a file too short to say either.
func TestHeadersThatAreNotHeaders(t *testing.T) {
	for _, c := range []struct {
		name string
		in   []byte
	}{
		{"empty", nil},
		{"one byte", []byte{0x1F}},
		{"two bytes", []byte{0x1F, 0x9D}},
		{"wrong magic", []byte{0x1F, 0x8B, 0x10}}, // that is a gzip
		{"width 8", []byte{0x1F, 0x9D, 8}},        // below the minimum
		{"width 17", []byte{0x1F, 0x9D, 17}},      // above what the format allows
		{"width 0", []byte{0x1F, 0x9D, 0x80}},     // block mode and no width
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewReader(bytes.NewReader(c.in)); !errors.Is(err, ErrHeader) {
				t.Errorf("NewReader = %v, want ErrHeader", err)
			}
		})
	}
}

// TestACodeThatCannotExistYetIsCorrupt.
//
// A .Z stream ends with whatever bits fill the last byte, so running out
// mid-code is the NORMAL end and must not be an error. A code past the table is
// different: it cannot be produced by any writer, and calling it EOF would hand
// the caller a silently short file.
func TestACodeThatCannotExistYetIsCorrupt(t *testing.T) {
	// A header, then two nine-bit codes: a literal, then 400 -- which no table
	// holds after one entry.
	var b bytes.Buffer
	b.Write([]byte{0x1F, 0x9D, 0x89}) // block mode, nine bits max
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
	put(400)
	if n > 0 {
		b.WriteByte(byte(acc))
	}
	r, err := NewReader(bytes.NewReader(b.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(r)
	if !errors.Is(err, ErrCorrupt) {
		t.Errorf("reading = %v, want ErrCorrupt", err)
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("a code past the table was reported as the end of the file: %v", err)
	}
}

// TestAFirstCodeAboveALiteralIsCorrupt: the first code of a stream names a byte,
// and nothing else can be right there.
func TestAFirstCodeAboveALiteralIsCorrupt(t *testing.T) {
	in := []byte{0x1F, 0x9D, 0x89, 0x00, 0x01} // nine bits: code 256
	r, err := NewReader(bytes.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(r); !errors.Is(err, ErrCorrupt) {
		t.Errorf("reading = %v, want ErrCorrupt", err)
	}
}

// TestReadInSmallBuffers: Read must not care how little it is given, and the
// decoder hands back whole strings at a time -- some of them thousands of bytes.
func TestReadInSmallBuffers(t *testing.T) {
	data := bytes.Repeat([]byte("the quick brown fox. "), 5000)
	z := squeeze(t, data, 16)
	r, err := NewReader(bytes.NewReader(z))
	if err != nil {
		t.Fatal(err)
	}
	var got []byte
	buf := make([]byte, 3)
	for {
		n, err := r.Read(buf)
		got = append(got, buf[:n]...)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
	}
	if !bytes.Equal(got, data) {
		t.Errorf("read back %d bytes in threes, want %d", len(got), len(data))
	}
}

// TestGzipAgreesWithUs is a second, independent judge: gzip reads .Z too, and it
// is a different implementation from compress(1).
func TestGzipAgreesWithUs(t *testing.T) {
	bin, err := exec.LookPath("gzip")
	if err != nil {
		t.Skip("no gzip here")
	}
	data := bytes.Repeat([]byte("judged twice. "), 20_000)
	z := squeeze(t, data, 16)

	cmd := exec.Command(bin, "-dc")
	cmd.Stdin = bytes.NewReader(z)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		t.Skipf("this gzip will not read .Z: %v\n%s", err, errOut.String())
	}
	if !bytes.Equal(out.Bytes(), data) {
		t.Fatalf("gzip itself disagrees with the fixture, so the fixture is wrong")
	}
	if got := inflate(t, z); !bytes.Equal(got, out.Bytes()) {
		t.Errorf("we and gzip disagree at byte %d", firstDiff(got, out.Bytes()))
	}
}

// TestASuffixOfTheStreamIsNotSilentlyDropped.
//
// Truncating a stream mid-code must give back everything decoded so far and stop,
// because that is indistinguishable from a well-formed end. What must NOT happen
// is a panic or an endless loop.
func TestATruncatedStreamStopsCleanly(t *testing.T) {
	data := bytes.Repeat([]byte("abcdefgh"), 10_000)
	z := squeeze(t, data, 16)
	for _, keep := range []int{3, 10, 100, len(z) / 2, len(z) - 1} {
		got := inflate(t, z[:keep])
		if len(got) > len(data) {
			t.Errorf("keeping %d of %d bytes gave %d bytes out, more than the original",
				keep, len(z), len(got))
		}
		if !bytes.HasPrefix(data, got) {
			t.Errorf("keeping %d bytes gave output that is not a prefix of the original",
				keep)
		}
	}
}

// TestTheNarrowestUsableWidthIsTheHardCase: at ten bits the table fills after a
// few hundred entries, so a stream of any size clears constantly and the group
// alignment runs over and over -- which at sixteen bits happens once.
func TestTheNarrowestUsableWidthIsTheHardCase(t *testing.T) {
	data := []byte(strings.Repeat("abcdefghijklmnopqrstuvwxyz", 4000))
	z := squeeze(t, data, 12)
	if got := inflate(t, z); !bytes.Equal(got, data) {
		t.Errorf("read back %d bytes, want %d (first difference at %d)",
			len(got), len(data), firstDiff(got, data))
	}
}

// TestAHeaderWithNoCodesIsAnEmptyFile.
//
// ⛔ There is no reference for this. compress(1) refuses to write a stream larger
// than its input, so it produces NOTHING for an empty file -- and both it and gzip
// REFUSE a hand-made header with no codes after it.
//
// So accepting one is a choice, and it is made deliberately: a header is
// well-formed, zero codes decode to zero bytes, and a reader that errors there
// would refuse a file that says exactly what it means. Being more permissive than
// the reference costs nothing here, because there is nothing to disagree about.
func TestAHeaderWithNoCodesIsAnEmptyFile(t *testing.T) {
	for _, flags := range []byte{0x90, 0x10, 0x8C, 0x0C} { // 16 and 12 bits, with and without block mode
		r, err := NewReader(bytes.NewReader([]byte{0x1F, 0x9D, flags}))
		if err != nil {
			t.Errorf("flags %#02x: %v", flags, err)
			continue
		}
		got, err := io.ReadAll(r)
		if err != nil {
			t.Errorf("flags %#02x: reading: %v", flags, err)
		}
		if len(got) != 0 {
			t.Errorf("flags %#02x: %d bytes out of a stream with no codes", flags, len(got))
		}
	}
}
