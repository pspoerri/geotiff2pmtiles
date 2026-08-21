//go:build windows

package tile

import (
	"syscall"
	"unsafe"
)

// memoryStatusEx mirrors the Win32 MEMORYSTATUSEX structure.
type memoryStatusEx struct {
	length               uint32
	memoryLoad           uint32
	totalPhys            uint64
	availPhys            uint64
	totalPageFile        uint64
	availPageFile        uint64
	totalVirtual         uint64
	availVirtual         uint64
	availExtendedVirtual uint64
}

// kernel32 is always already loaded into the process, so this resolves the
// in-memory module rather than searching the DLL path.
var procGlobalMemoryStatusEx = syscall.NewLazyDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")

// totalSystemRAM returns the total physical RAM in bytes on Windows.
func totalSystemRAM() (uint64, error) {
	var ms memoryStatusEx
	ms.length = uint32(unsafe.Sizeof(ms))
	r, _, err := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&ms)))
	if r == 0 {
		return 0, err
	}
	return ms.totalPhys, nil
}
