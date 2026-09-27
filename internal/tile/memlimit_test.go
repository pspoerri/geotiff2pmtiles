package tile

import (
	"errors"
	"testing"
)

// The auto memory limit must never be 0, which would turn spilling off and
// keep every tile of a level in RAM on exactly the small machines that
// cannot afford it.
func TestMemoryLimitFor(t *testing.T) {
	const GB = 1 << 30
	overhead := uint64(2*GB + 50<<20) // 2 GB headroom + runtime Sys
	frac := 0.9
	tests := []struct {
		name     string
		totalRAM uint64
		err      error
		want     int64
	}{
		{"undetectable", 0, errors.New("unsupported"), minMemoryLimit},
		{"2 GB VM, formerly off", 2 * GB, nil, minMemoryLimit},
		{"2.8 GB, formerly off", 2800 << 20, nil, int64(float64(2800<<20)*frac) - int64(overhead)},
		{"4 GB container", 4 * GB, nil, int64(float64(4*GB)*frac) - int64(overhead)},
		{"64 GB", 64 * GB, nil, int64(float64(64*GB)*frac) - int64(overhead)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := memoryLimitFor(tt.totalRAM, tt.err, overhead, frac); got != tt.want {
				t.Errorf("memoryLimitFor = %d MB, want %d MB", got>>20, tt.want>>20)
			}
		})
	}
}

func TestComputeMemoryLimit_NeverDisablesSpilling(t *testing.T) {
	if got := ComputeMemoryLimit(DefaultMemoryPressurePercent, false); got < minMemoryLimit {
		t.Errorf("ComputeMemoryLimit = %d, want at least %d", got, minMemoryLimit)
	}
}

func TestParseCgroupLimit(t *testing.T) {
	tests := []struct {
		in     string
		want   uint64
		wantOK bool
	}{
		{"4294967296\n", 4 << 30, true},               // v2 or v1 with a limit
		{"max\n", 0, false},                           // v2, no limit
		{"9223372036854771712\n", 1<<63 - 4096, true}, // v1, no limit
		{"", 0, false},
		{"0\n", 0, false},
	}
	for _, tt := range tests {
		got, ok := parseCgroupLimit([]byte(tt.in))
		if got != tt.want || ok != tt.wantOK {
			t.Errorf("parseCgroupLimit(%q) = %d, %v; want %d, %v", tt.in, got, ok, tt.want, tt.wantOK)
		}
	}
}
