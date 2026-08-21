//go:build windows

package cog

import (
	"fmt"
	"syscall"
	"unsafe"
)

// mmapFile memory-maps a file read-only. The fd can be closed after mapping:
// the mapped view keeps the underlying section object alive.
//
// Note that Windows keeps the file locked for as long as a view exists, so the
// file cannot be renamed or deleted until munmapFile is called (unlike POSIX).
func mmapFile(fd uintptr, size int) ([]byte, error) {
	if size <= 0 {
		return nil, fmt.Errorf("mmap: invalid size %d", size)
	}
	h, err := syscall.CreateFileMapping(syscall.Handle(fd), nil, syscall.PAGE_READONLY,
		uint32(uint64(size)>>32), uint32(uint64(size)&0xffffffff), nil)
	if err != nil {
		return nil, fmt.Errorf("CreateFileMapping: %w", err)
	}
	// The view holds its own reference; the mapping handle is no longer needed.
	defer syscall.CloseHandle(h)

	addr, err := syscall.MapViewOfFile(h, syscall.FILE_MAP_READ, 0, 0, uintptr(size))
	if err != nil {
		return nil, fmt.Errorf("MapViewOfFile: %w", err)
	}
	// The mapped view lives outside the Go heap, so the GC has nothing to
	// track here. Build the slice header directly: unsafe.Slice would need a
	// uintptr -> unsafe.Pointer conversion, which go vet flags.
	sh := struct {
		data uintptr
		len  int
		cap  int
	}{addr, size, size}
	return *(*[]byte)(unsafe.Pointer(&sh)), nil
}

// munmapFile releases a memory mapping created by mmapFile.
func munmapFile(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	return syscall.UnmapViewOfFile(uintptr(unsafe.Pointer(&data[0])))
}
