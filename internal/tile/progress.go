package tile

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// progressBar renders an in-place terminal progress bar for a zoom level.
// It refreshes at a fixed interval and supports concurrent Increment calls
// from multiple worker goroutines.
type progressBar struct {
	total     int64
	processed atomic.Int64
	label     string
	barWidth  int
	start     time.Time
	done      chan struct{}
	mu        sync.Mutex
}

func newProgressBar(label string, total int64) *progressBar {
	pb := &progressBar{
		total:    total,
		label:    label,
		barWidth: 30,
		start:    time.Now(),
		done:     make(chan struct{}),
	}
	go pb.run()
	return pb
}

// Increment marks one more item as processed. Safe for concurrent use.
func (pb *progressBar) Increment() {
	pb.processed.Add(1)
}

// Finish stops the refresh loop and prints the final bar state with a newline.
func (pb *progressBar) Finish() {
	close(pb.done)
	pb.draw()
	fmt.Fprint(os.Stderr, "\n")
}

func (pb *progressBar) run() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-pb.done:
			return
		case <-ticker.C:
			pb.draw()
		}
	}
}

func (pb *progressBar) draw() {
	pb.mu.Lock()
	defer pb.mu.Unlock()

	processed := pb.processed.Load()
	total := pb.total

	var frac float64
	if total > 0 {
		frac = float64(processed) / float64(total)
	}
	if frac > 1 {
		frac = 1
	}

	filled := int(float64(pb.barWidth) * frac)
	bar := strings.Repeat("█", filled) + strings.Repeat("░", pb.barWidth-filled)

	elapsed := time.Since(pb.start)
	rate := float64(0)
	if secs := elapsed.Seconds(); secs > 0 {
		rate = float64(processed) / secs
	}

	fmt.Fprintf(os.Stderr, "\r%s [%s] %3.0f%%  %d/%d tiles  %.0f/s  %s\033[K",
		pb.label, bar, frac*100, processed, total, rate, formatDuration(elapsed))
}

// formatDuration formats a duration concisely (e.g. "1m23s", "45s", "0s").
func formatDuration(d time.Duration) string {
	d = d.Truncate(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	m := int(d.Minutes())
	s := int(d.Seconds()) - m*60
	return fmt.Sprintf("%dm%02ds", m, s)
}
