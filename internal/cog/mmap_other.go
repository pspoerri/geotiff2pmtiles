//go:build !unix

package cog

import "fmt"

// mmapFile is not supported on non-Unix platforms.
func mmapFile(fd uintptr, size int) ([]byte, error) {
	return nil, fmt.Errorf("memory mapping is not supported on this platform")
}

// munmapFile is a no-op on non-Unix platforms.
func munmapFile(data []byte) error {
	return nil
}
