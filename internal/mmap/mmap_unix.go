//go:build unix

// Package mmap memory-maps files read-only (Unix and Windows).
package mmap

import "syscall"

// Map memory-maps a file read-only. The fd can be closed after mapping.
func Map(fd uintptr, size int) ([]byte, error) {
	return syscall.Mmap(int(fd), 0, size, syscall.PROT_READ, syscall.MAP_PRIVATE)
}

// Unmap releases a memory mapping created by Map.
func Unmap(data []byte) error {
	return syscall.Munmap(data)
}
