package cog

// TIFF-compatible LZW decoder.
//
// TIFF uses a LZW variant that differs from the GIF/PDF format handled by Go's
// compress/lzw package. The key difference is the "deferred increment" of code
// width: TIFF increments the width after emitting the code that fills the
// current width, while GIF increments it before. Go's compress/lzw implements
// the GIF variant, causing "invalid code" errors on TIFF LZW streams.
//
// This implementation follows the TIFF 6.0 specification for LZW compression.

import (
	"errors"
	"io"
)

const (
	lzwMaxWidth  = 12
	lzwClearCode = 256
	lzwEOICode   = 257
	lzwFirstCode = 258
)

type lzwEntry struct {
	prefix int    // index of prefix entry (-1 for single-byte entries)
	suffix byte   // the byte added by this entry
	length int    // total length of the string
}

// decompressTIFFLZW decompresses TIFF-style LZW data (MSB bit ordering).
func decompressTIFFLZW(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, nil
	}

	d := &lzwDecoder{
		src:    data,
		bitPos: 0,
	}

	return d.decode()
}

type lzwDecoder struct {
	src    []byte
	bitPos int // current bit position in src
}

// readBits reads n bits from the source (MSB first).
func (d *lzwDecoder) readBits(n int) (int, error) {
	if n <= 0 || n > 16 {
		return 0, errors.New("lzw: invalid bit count")
	}

	result := 0
	for i := 0; i < n; i++ {
		bytePos := d.bitPos / 8
		bitOff := 7 - (d.bitPos % 8) // MSB first
		if bytePos >= len(d.src) {
			return 0, io.ErrUnexpectedEOF
		}
		bit := (int(d.src[bytePos]) >> bitOff) & 1
		result = (result << 1) | bit
		d.bitPos++
	}
	return result, nil
}

func (d *lzwDecoder) decode() ([]byte, error) {
	// Initialize the code table with all single-byte entries.
	// Pre-allocate for max 12-bit codes (4096 entries).
	table := make([]lzwEntry, 4097)
	for i := 0; i < 256; i++ {
		table[i] = lzwEntry{prefix: -1, suffix: byte(i), length: 1}
	}
	// Clear code and EOI code occupy entries 256 and 257.

	nextCode := lzwFirstCode
	codeWidth := 9

	var output []byte
	buf := make([]byte, 0, 4096)

	// Helper: extract the string for a given code into buf (reversed, then flipped).
	getString := func(code int) []byte {
		entry := &table[code]
		buf = buf[:entry.length]
		idx := entry.length - 1
		for code >= 0 {
			e := &table[code]
			buf[idx] = e.suffix
			idx--
			code = e.prefix
		}
		return buf
	}

	// First code must be a clear code per TIFF spec.
	code, err := d.readBits(codeWidth)
	if err != nil {
		return nil, err
	}
	if code != lzwClearCode {
		return nil, errors.New("lzw: first code is not clear code")
	}

	// After clear code, read the first literal.
	prevCode := -1

	for {
		code, err := d.readBits(codeWidth)
		if err != nil {
			if err == io.ErrUnexpectedEOF {
				return output, nil
			}
			return nil, err
		}

		if code == lzwEOICode {
			return output, nil
		}

		if code == lzwClearCode {
			// Reset.
			nextCode = lzwFirstCode
			codeWidth = 9
			prevCode = -1
			continue
		}

		if prevCode == -1 {
			// First code after clear: must be a literal (0-255).
			if code >= 256 {
				return nil, errors.New("lzw: first code after clear is not literal")
			}
			output = append(output, byte(code))
			prevCode = code
			continue
		}

		var outStr []byte

		if code < nextCode {
			// Code is in the table.
			outStr = getString(code)
			output = append(output, outStr...)

			// Add new entry: prevCode's string + first byte of current string.
			if nextCode < 4097 {
				table[nextCode] = lzwEntry{
					prefix: prevCode,
					suffix: outStr[0],
					length: table[prevCode].length + 1,
				}
				nextCode++
			}
		} else if code == nextCode {
			// KwKwK case: code is not yet in the table.
			prevStr := getString(prevCode)
			firstByte := prevStr[0]
			output = append(output, prevStr...)
			output = append(output, firstByte)

			// Add new entry.
			if nextCode < 4097 {
				table[nextCode] = lzwEntry{
					prefix: prevCode,
					suffix: firstByte,
					length: table[prevCode].length + 1,
				}
				nextCode++
			}
		} else {
			return nil, errors.New("lzw: invalid code")
		}

		// Increase code width when the next possible entry would exceed
		// the current width's capacity.
		if nextCode+1 >= (1<<codeWidth) && codeWidth < lzwMaxWidth {
			codeWidth++
		}

		prevCode = code
	}
}
