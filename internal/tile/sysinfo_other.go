//go:build !darwin && !linux

package tile

import "fmt"

// totalSystemRAM is unsupported on this platform.
func totalSystemRAM() (uint64, error) {
	return 0, fmt.Errorf("unsupported platform for RAM detection")
}
