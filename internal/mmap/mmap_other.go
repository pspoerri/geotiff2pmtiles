//go:build !unix && !windows

package mmap

import "fmt"

// Map is not supported on non-Unix platforms.
func Map(fd uintptr, size int) ([]byte, error) {
	return nil, fmt.Errorf("memory mapping is not supported on this platform")
}

// Unmap is a no-op on non-Unix platforms.
func Unmap(data []byte) error {
	return nil
}
