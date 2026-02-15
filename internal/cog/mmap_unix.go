//go:build unix

package cog

import "syscall"

// mmapFile memory-maps a file read-only. The fd can be closed after mapping.
func mmapFile(fd uintptr, size int) ([]byte, error) {
	return syscall.Mmap(int(fd), 0, size, syscall.PROT_READ, syscall.MAP_PRIVATE)
}

// munmapFile releases a memory mapping created by mmapFile.
func munmapFile(data []byte) error {
	return syscall.Munmap(data)
}
