# compress

Reads the **`.Z`** format of `compress(1)` — pure Go, `CGO_ENABLED=0`, builds for
every target Go builds for.

```go
r, err := compress.NewReader(f)
if err != nil { return err }
io.Copy(dst, r)
```

## Why the standard library cannot

`compress/lzw` says what it is for: *"LZW as used by the GIF and PDF file formats,
which means variable-width codes up to 12 bits"*. `.Z` needs three things it does
not have:

| | |
|---|---|
| codes up to **16 bits** | `compress/lzw` stops at 12 |
| a **clear code** | the writer resets the table mid-stream when compression stops paying |
| the **group alignment** | codes are written in groups of eight, and the stream is *padded* to the end of a group before the width grows or the table clears |

The last one is what makes `.Z` hard. It is invisible in small files: a reader
that ignores it decodes everything up to the first widening and then produces
**plausible rubbish**, which is worse than failing.

## There is no writer, on purpose

Reading `.Z` opens archives that already exist. Writing one makes a file nobody
should be making in 2026 — gzip, xz and zstd all compress better and are read
everywhere. The format is here to be understood, not perpetuated.

## ⛔ The fixture generator was broken before the reader was

**macOS's `compress(1)` writes streams its own `-d` refuses**, at `-b 9`, `-b 10`
and `-b 11`. It exits 0 doing it, and `gzip -d` refuses them too:

```
$ printf 'x' | compress -b 9 -c > t.Z ; compress -dc < t.Z
compress: Inappropriate file type or format
```

Three of those were used as fixtures at first, and **every failure they caused was
reported against this reader**, which was right to refuse them. Hours went into
chasing a defect that was in the generator.

So `squeeze` now asks the reference to read back what it just wrote, and skips a
width it cannot. The fixture is checked before the subject.

The other half of the same trap: measuring this from a shell gave *"every width
fails"*, because `compress -d` refuses a file whose name does not end in `.Z` —
a failure for a reason that had nothing to do with the bytes.

## What the tests assert beyond "it decodes"

- that the width **actually grew** — a corpus that never widens tests nothing
  while looking thorough (it ends at 12 bits with 3550 table entries);
- that **block mode was exercised**, which only data defeating LZW provokes;
- that a code past the table is `ErrCorrupt` and **not** `io.EOF`, because a `.Z`
  ends with whatever bits fill its last byte, so running out mid-code is the
  normal end and calling damage "the end" hands back a silently short file.

100% statement coverage, race clean, 8 CI lanes including 4 emulated
architectures.

## Licence

BSD-3-Clause.
