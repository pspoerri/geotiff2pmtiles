// Package cli holds the command-line helpers that geotiff2pmtiles and
// pmtransform share: colour and size formatting, profiling flags and the
// per-run temp directory.
package cli

import (
	"flag"
	"fmt"
	"image/color"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"syscall"
)

// ParseColor parses an RGBA color from "R,G,B,A", "#RRGGBB" or "#RRGGBBAA".
func ParseColor(s string) (color.RGBA, error) {
	if strings.HasPrefix(s, "#") {
		return parseHexColor(s)
	}

	parts := strings.Split(s, ",")
	if len(parts) != 4 {
		return color.RGBA{}, fmt.Errorf("expected R,G,B,A format (e.g. \"0,0,0,255\"), got %q", s)
	}

	var vals [4]uint8
	for i, p := range parts {
		v, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil || v < 0 || v > 255 {
			return color.RGBA{}, fmt.Errorf("invalid color component %q (must be 0-255)", p)
		}
		vals[i] = uint8(v)
	}
	return color.RGBA{R: vals[0], G: vals[1], B: vals[2], A: vals[3]}, nil
}

func parseHexColor(s string) (color.RGBA, error) {
	s = strings.TrimPrefix(s, "#")
	switch len(s) {
	case 6:
		s += "ff" // opaque
	case 8:
		// full RRGGBBAA
	default:
		return color.RGBA{}, fmt.Errorf("hex color must be #RRGGBB or #RRGGBBAA, got %q", "#"+s)
	}

	var vals [4]uint8
	for i := range vals {
		v, err := strconv.ParseUint(s[2*i:2*i+2], 16, 8)
		if err != nil {
			return color.RGBA{}, fmt.Errorf("invalid hex color: %w", err)
		}
		vals[i] = uint8(v)
	}
	return color.RGBA{R: vals[0], G: vals[1], B: vals[2], A: vals[3]}, nil
}

// ParseOptionalColor is ParseColor for flags that can be turned off: ""
// and "none" give nil.
func ParseOptionalColor(s string) (*color.RGBA, error) {
	if s == "" || strings.EqualFold(s, "none") {
		return nil, nil
	}
	c, err := ParseColor(s)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// HumanSize formats a byte count with a binary unit, e.g. "1.5 MB".
func HumanSize(bytes int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)
	switch {
	case bytes >= GB:
		return fmt.Sprintf("%.1f GB", float64(bytes)/float64(GB))
	case bytes >= MB:
		return fmt.Sprintf("%.1f MB", float64(bytes)/float64(MB))
	case bytes >= KB:
		return fmt.Sprintf("%.1f KB", float64(bytes)/float64(KB))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

// IsFlagSet reports whether the flag name was passed on the command line
// parsed by fs, as opposed to keeping its default.
func IsFlagSet(fs *flag.FlagSet, name string) bool {
	set := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}

// Profiles holds the paths given to --cpu-profile and --mem-profile.
type Profiles struct {
	cpu, mem string
}

// RegisterProfileFlags registers --cpu-profile and --mem-profile on fs,
// plus the old -cpuprofile and -memprofile spellings as deprecated aliases.
func RegisterProfileFlags(fs *flag.FlagSet) *Profiles {
	p := &Profiles{}
	fs.StringVar(&p.cpu, "cpu-profile", "", "Write a CPU profile to `file`")
	fs.StringVar(&p.mem, "mem-profile", "", "Write a heap profile to `file` at exit")
	fs.StringVar(&p.cpu, "cpuprofile", "", "deprecated: use --cpu-profile")
	fs.StringVar(&p.mem, "memprofile", "", "deprecated: use --mem-profile")
	return p
}

// Start begins CPU profiling if it was requested. The returned stop ends it
// and writes the heap profile; call it on every exit path that should keep
// the profiles (log.Fatal skips deferred calls).
func (p *Profiles) Start() (stop func() error, err error) {
	var cpuFile *os.File
	if p.cpu != "" {
		if cpuFile, err = os.Create(p.cpu); err != nil {
			return nil, fmt.Errorf("creating CPU profile: %w", err)
		}
		if err := pprof.StartCPUProfile(cpuFile); err != nil {
			cpuFile.Close()
			return nil, fmt.Errorf("starting CPU profile: %w", err)
		}
	}
	return func() error {
		if cpuFile != nil {
			pprof.StopCPUProfile()
			if err := cpuFile.Close(); err != nil {
				return fmt.Errorf("writing CPU profile: %w", err)
			}
			cpuFile = nil
		}
		if p.mem == "" {
			return nil
		}
		f, err := os.Create(p.mem)
		if err != nil {
			return fmt.Errorf("creating memory profile: %w", err)
		}
		runtime.GC() // up-to-date statistics
		if err := pprof.WriteHeapProfile(f); err != nil {
			f.Close()
			return fmt.Errorf("writing memory profile: %w", err)
		}
		return f.Close()
	}, nil
}

// MakeTmpDir creates a per-run directory for temporary tile and spill files
// under dir (default: the output file's directory), named prefix plus a
// random suffix, and warns about directories with the same prefix that an
// earlier run killed without cleanup (SIGKILL, out of memory) left behind.
//
// The returned cleanup removes the directory and <outputPath>.partial, the
// archive the PMTiles writer assembles next to the output while finalizing.
// It also runs on SIGINT/SIGTERM, so an interrupted run does not leave
// gigabytes of temp files behind. log.Fatal skips defers, so fatal paths
// after this call must run cleanup themselves.
func MakeTmpDir(dir, outputPath, prefix string) (string, func(), error) {
	if dir == "" {
		dir = filepath.Dir(outputPath)
	}
	if stale, _ := filepath.Glob(filepath.Join(dir, prefix+"*")); len(stale) > 0 {
		log.Printf("WARNING: %s holds %d temp dir(s) left by earlier runs (e.g. %s); delete them unless another run is still using them",
			dir, len(stale), filepath.Base(stale[0]))
	}
	tmp, err := os.MkdirTemp(dir, prefix+"*")
	if err != nil {
		return "", nil, fmt.Errorf("creating temp directory: %w", err)
	}
	cleanup := func() {
		os.RemoveAll(tmp)
		os.Remove(outputPath + ".partial")
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		cleanup()
		log.Printf("Interrupted; removed temp directory %s", tmp)
		os.Exit(130)
	}()
	return tmp, cleanup, nil
}
