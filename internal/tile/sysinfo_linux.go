//go:build linux

package tile

import "syscall"

// totalSystemRAM returns the total physical RAM in bytes on Linux.
func totalSystemRAM() (uint64, error) {
	var info syscall.Sysinfo_t
	if err := syscall.Sysinfo(&info); err != nil {
		return 0, err
	}
	return info.Totalram * uint64(info.Unit), nil
}
