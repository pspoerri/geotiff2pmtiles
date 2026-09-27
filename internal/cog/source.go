package cog

import (
	"fmt"
	"io"
)

// ByteSource is the random-access byte store backing a Reader.
//
// A Reader used to index a memory-mapped []byte directly, which ties it to
// local files. Reading COGs straight out of object storage needs the same
// reader over HTTP range requests, so every byte access goes through this
// interface instead. mmapSource keeps the local path zero-copy, so nothing
// regresses for on-disk inputs.
type ByteSource interface {
	// Size reports the total length of the source in bytes.
	Size() int64
	// Slice returns the bytes in [off, end). The result must not be mutated
	// by the caller, and stays valid for as long as it is held. Slice is
	// called concurrently by the tile generator's worker pool, so
	// implementations must be safe for concurrent use.
	Slice(off, end uint64) ([]byte, error)
	io.Closer
}

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

// sourceReadSeeker adapts a ByteSource to the io.ReadSeeker that parseTIFF
// wants. Only the header and IFD chain are read through it, so the naive
// per-call slicing costs nothing measurable.
type sourceReadSeeker struct {
	src ByteSource
	pos int64
}

func (s *sourceReadSeeker) Read(p []byte) (int, error) {
	size := s.src.Size()
	if s.pos >= size {
		return 0, io.EOF
	}
	end := s.pos + int64(len(p))
	if end > size {
		end = size
	}
	b, err := s.src.Slice(uint64(s.pos), uint64(end))
	if err != nil {
		return 0, err
	}
	n := copy(p, b)
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
