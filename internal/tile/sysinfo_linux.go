//go:build linux

package tile

import (
	"os"
	"syscall"
)

// cgroupMemoryFiles hold the memory limit of the process's cgroup when it
// runs in its own cgroup namespace, as in Docker or Kubernetes.
var cgroupMemoryFiles = []string{
	"/sys/fs/cgroup/memory.max",                   // cgroup v2
	"/sys/fs/cgroup/memory/memory.limit_in_bytes", // cgroup v1
}

// totalSystemRAM returns the total physical RAM in bytes on Linux, or the
// cgroup memory limit when that is lower: sysinfo reports the host's RAM
// inside a container, and filling it would get the process OOM-killed.
func totalSystemRAM() (uint64, error) {
	var info syscall.Sysinfo_t
	if err := syscall.Sysinfo(&info); err != nil {
		return 0, err
	}
	total := uint64(info.Totalram) * uint64(info.Unit)
	for _, path := range cgroupMemoryFiles {
		if data, err := os.ReadFile(path); err == nil {
			if limit, ok := parseCgroupLimit(data); ok && limit < total {
				total = limit
			}
		}
	}
	return total, nil
}
