//go:build darwin

package tile

import (
	"syscall"
	"unsafe"
)

// totalSystemRAM returns the total physical RAM in bytes on macOS.
func totalSystemRAM() (uint64, error) {
	mib := [2]int32{6 /* CTL_HW */, 24 /* HW_MEMSIZE */}
	var size uint64
	n := uintptr(8) // sizeof(uint64)
	_, _, errno := syscall.Syscall6(
		syscall.SYS___SYSCTL,
		uintptr(unsafe.Pointer(&mib[0])),
		2,
		uintptr(unsafe.Pointer(&size)),
		uintptr(unsafe.Pointer(&n)),
		0, 0,
	)
	if errno != 0 {
		return 0, errno
	}
	return size, nil
}
