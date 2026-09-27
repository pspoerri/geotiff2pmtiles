package tile

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// isTerminal reports whether f is a terminal or console rather than a file
// or pipe, where every \r redraw would be kept as a separate frame.
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// progressBar renders an in-place terminal progress bar for a zoom level.
// It refreshes at a fixed interval and supports concurrent Increment calls
// from multiple worker goroutines. When the output is not a terminal it
// prints only the final state, one line per zoom level.
type progressBar struct {
	out       io.Writer
	live      bool          // redraw in place every 100 ms
	done      chan struct{} // closed by Finish
	stopped   chan struct{} // closed when run has returned
	start     time.Time
	label     string
	total     int64
	processed atomic.Int64
	barWidth  int
	prevWidth int // rune width of the last drawn line, for padding
	mu        sync.Mutex
}

// newProgressBar starts a progress bar on stderr, redrawn in place only when
// stderr is a terminal.
func newProgressBar(label string, total int64) *progressBar {
	return startProgressBar(os.Stderr, isTerminal(os.Stderr), label, total)
}

func startProgressBar(out io.Writer, live bool, label string, total int64) *progressBar {
	pb := &progressBar{
		out:      out,
		live:     live,
		total:    total,
		label:    label,
		barWidth: 30,
		start:    time.Now(),
		done:     make(chan struct{}),
		stopped:  make(chan struct{}),
	}
	if pb.live {
		go pb.run()
	} else {
		close(pb.stopped)
	}
	return pb
}

// Increment marks one more item as processed. Safe for concurrent use.
func (pb *progressBar) Increment() {
	pb.processed.Add(1)
}

// Finish stops the refresh loop and prints the final bar state with a newline.
func (pb *progressBar) Finish() {
	close(pb.done)
	<-pb.stopped // no redraw may follow the final line
	pb.draw()
	fmt.Fprint(pb.out, "\n")
}

func (pb *progressBar) run() {
	defer close(pb.stopped)
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

	// Compute ETA from current throughput.
	etaStr := "—"
	remaining := total - processed
	if rate > 0 && remaining > 0 {
		eta := time.Duration(float64(remaining)/rate) * time.Second
		etaStr = formatDuration(eta)
	} else if remaining <= 0 {
		etaStr = "0s"
	}

	line := fmt.Sprintf("%s [%s] %3.0f%%  %d/%d tiles  %.0f/s  %s  ETA %s",
		pb.label, bar, frac*100, processed, total, rate, formatDuration(elapsed), etaStr)

	if !pb.live {
		fmt.Fprint(pb.out, line)
		return
	}

	// Pad the line out instead of using an ANSI erase-to-end-of-line: Windows
	// consoles ignore escape sequences unless VT processing is switched on.
	width := utf8.RuneCountInString(line)
	if pad := pb.prevWidth - width; pad > 0 {
		line += strings.Repeat(" ", pad)
	}
	pb.prevWidth = width

	fmt.Fprintf(pb.out, "\r%s", line)
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
