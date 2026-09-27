package cog

import (
	"errors"
	"fmt"
	"io"
)

// ByteSource is the random-access byte store backing a Reader.
//
// A Reader used to index a memory-mapped []byte directly, which ties it to
// local files. Reading COGs straight out of object storage needs the same
// reader over HTTP range requests, so every byte access goes through this
// interface instead. mmapSource keeps the local path zero-copy, so nothing
// regresses for on-disk inputs. OpenSource reads the header and IFDs in
// 64 KiB blocks; after that, each tile or strip read is one Slice call.
type ByteSource interface {
	// Size reports the total length of the source in bytes.
	Size() int64
	// Slice returns the bytes in [off, end); the Reader only asks for
	// 0 <= off <= end <= Size(), however corrupt the file's offsets are.
	// The result must not be mutated, and is only valid until Close (for
	// mmapSource it is a view of the mapping): callers that need the bytes
	// afterwards must copy them. Slice is called concurrently by the tile
	// generator's worker pool, so implementations must be safe for
	// concurrent use.
	Slice(off, end uint64) ([]byte, error)
	io.Closer
}

// slice returns the size bytes at offset. The range comes from the file, so
// it is checked against the source without overflow before Slice sees it.
// what names the data in the error.
func (r *Reader) slice(what string, offset, size uint64) ([]byte, error) {
	n := uint64(r.src.Size())
	if offset > n || size > n-offset {
		if _, closed := r.src.(closedSource); closed {
			return nil, errClosed
		}
		return nil, fmt.Errorf("%s [%d:+%d] exceeds file size %d", what, offset, size, n)
	}
	return r.src.Slice(offset, offset+size)
}

// errClosed is returned by reads from a closed Reader.
var errClosed = errors.New("cog: reader is closed")

// closedSource replaces a Reader's source on Close, so a later read fails
// with an error instead of dereferencing a nil source.
type closedSource struct{}

func (closedSource) Size() int64                           { return 0 }
func (closedSource) Slice(off, end uint64) ([]byte, error) { return nil, errClosed }
func (closedSource) Close() error                          { return nil }

// mmapSource is a ByteSource over a memory-mapped file. Slice hands back a
// subslice of the mapping, so local reads stay allocation-free.
type mmapSource []byte

func (m mmapSource) Size() int64 { return int64(len(m)) }

func (m mmapSource) Slice(off, end uint64) ([]byte, error) {
	if end > uint64(len(m)) || off > end {
		return nil, fmt.Errorf("range [%d:%d] outside source of %d bytes", off, end, len(m))
	}
	return m[off:end], nil
}

func (m mmapSource) Close() error {
	if m == nil {
		return nil
	}
	return munmapFile(m)
}

// readAhead is the block size sourceReadSeeker fetches per Slice call.
const readAhead = 64 << 10

// sourceReadSeeker adapts a ByteSource to the io.ReadSeeker that parseTIFF
// wants. The parser reads a few bytes per directory entry, about 150 reads
// for a typical COG; over a remote source each Slice is a round trip, so
// reads are served from a read-ahead block instead. GDAL COGs keep the
// header and all IFDs in the first few KiB, so opening one takes one Slice.
type sourceReadSeeker struct {
	src    ByteSource
	pos    int64
	buf    []byte // read-ahead block starting at bufOff
	bufOff int64
}

func (s *sourceReadSeeker) Read(p []byte) (int, error) {
	size := s.src.Size()
	if s.pos >= size {
		return 0, io.EOF
	}
	if s.pos < s.bufOff || s.pos >= s.bufOff+int64(len(s.buf)) {
		n := int64(len(p))
		if n < readAhead {
			n = readAhead
		}
		if n > size-s.pos {
			n = size - s.pos
		}
		b, err := s.src.Slice(uint64(s.pos), uint64(s.pos+n))
		if err != nil {
			return 0, err
		}
		s.buf, s.bufOff = b, s.pos
	}
	n := copy(p, s.buf[s.pos-s.bufOff:])
	s.pos += int64(n)
	return n, nil
}

func (s *sourceReadSeeker) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = s.pos + offset
	case io.SeekEnd:
		abs = s.src.Size() + offset
	default:
		return 0, fmt.Errorf("invalid whence %d", whence)
	}
	if abs < 0 {
		return 0, fmt.Errorf("negative seek position %d", abs)
	}
	s.pos = abs
	return abs, nil
}
