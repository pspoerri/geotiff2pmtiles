package tile

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// Redirected to a file or pipe, a progress bar must print one line per zoom
// level: \r redraws every 100 ms pile up as ~36k frames an hour in a log.
func TestProgressBar_NotTerminal_OneLine(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	stderr := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = stderr }()

	pb := newProgressBar("Zoom  3", 4)
	for i := 0; i < 4; i++ {
		pb.Increment()
	}
	time.Sleep(250 * time.Millisecond) // two ticks of a live bar
	pb.Finish()
	os.Stderr = stderr
	w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}

	got := string(out)
	if strings.Contains(got, "\r") {
		t.Errorf("output has carriage returns: %q", got)
	}
	if n := strings.Count(got, "\n"); n != 1 || !strings.HasSuffix(got, "\n") {
		t.Errorf("want exactly one line, got %q", got)
	}
	if !strings.HasPrefix(got, "Zoom  3 [") || !strings.Contains(got, "4/4 tiles") {
		t.Errorf("unexpected line %q", got)
	}
}

func TestProgressBar_Terminal_Redraws(t *testing.T) {
	var buf bytes.Buffer
	pb := startProgressBar(&buf, true, "Zoom  3", 4)
	time.Sleep(250 * time.Millisecond)
	pb.Finish()

	got := buf.String()
	if strings.Count(got, "\r") < 2 {
		t.Errorf("want in-place redraws on a terminal, got %q", got)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("final line not terminated: %q", got)
	}
}
