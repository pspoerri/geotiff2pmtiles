package cli

import (
	"flag"
	"image/color"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestParseColor(t *testing.T) {
	tests := []struct {
		in      string
		want    color.RGBA
		wantErr bool
	}{
		{"0,0,0,0", color.RGBA{}, false},
		{"255, 128, 1, 200", color.RGBA{255, 128, 1, 200}, false},
		{"#ff8001", color.RGBA{255, 128, 1, 255}, false},
		{"#FF8001c8", color.RGBA{255, 128, 1, 200}, false},
		{"1,2,3", color.RGBA{}, true},
		{"1,2,3,256", color.RGBA{}, true},
		{"1,2,3,-1", color.RGBA{}, true},
		{"#12345", color.RGBA{}, true},
		{"#gg0000", color.RGBA{}, true},
		{"none", color.RGBA{}, true},
	}
	for _, tt := range tests {
		got, err := ParseColor(tt.in)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("ParseColor(%q) = %v, %v; want %v, error %v", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestParseOptionalColor(t *testing.T) {
	for _, s := range []string{"", "none", "NONE"} {
		if c, err := ParseOptionalColor(s); c != nil || err != nil {
			t.Errorf("ParseOptionalColor(%q) = %v, %v; want nil, nil", s, c, err)
		}
	}
	if c, err := ParseOptionalColor("0,0,0,0"); err != nil || c == nil || *c != (color.RGBA{}) {
		t.Errorf("ParseOptionalColor(0,0,0,0) = %v, %v; want transparent black", c, err)
	}
	if _, err := ParseOptionalColor("black"); err == nil {
		t.Error("ParseOptionalColor(black): want an error")
	}
}

func TestHumanSize(t *testing.T) {
	for in, want := range map[int64]string{
		0:           "0 B",
		1023:        "1023 B",
		1536:        "1.5 KB",
		5 << 20:     "5.0 MB",
		3 << 30 / 2: "1.5 GB",
	} {
		if got := HumanSize(in); got != want {
			t.Errorf("HumanSize(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestIsFlagSet(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.String("a", "x", "")
	fs.String("b", "x", "")
	if err := fs.Parse([]string{"-a", "x"}); err != nil {
		t.Fatal(err)
	}
	if !IsFlagSet(fs, "a") {
		t.Error("a was passed (with its default value) but IsFlagSet says no")
	}
	if IsFlagSet(fs, "b") {
		t.Error("b was not passed but IsFlagSet says yes")
	}
}

func TestProfileFlags(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{
		{"--cpu-profile", filepath.Join(dir, "cpu1"), "--mem-profile", filepath.Join(dir, "mem1")},
		{"-cpuprofile", filepath.Join(dir, "cpu2"), "-memprofile", filepath.Join(dir, "mem2")}, // deprecated aliases
	} {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		p := RegisterProfileFlags(fs)
		if err := fs.Parse(args); err != nil {
			t.Fatal(err)
		}
		stop, err := p.Start()
		if err != nil {
			t.Fatal(err)
		}
		if err := stop(); err != nil {
			t.Fatal(err)
		}
		for _, f := range []string{args[1], args[3]} {
			if fi, err := os.Stat(f); err != nil || fi.Size() == 0 {
				t.Errorf("%v: profile %s not written (%v)", args, f, err)
			}
		}
	}

	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	RegisterProfileFlags(fs)
	fs.PrintDefaults() // must not panic on the backquoted placeholders
	if f := fs.Lookup("cpuprofile"); f == nil || f.Usage != "deprecated: use --cpu-profile" {
		t.Errorf("-cpuprofile help = %v", f)
	}
}

func TestProfileFlagsUnset(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	p := RegisterProfileFlags(fs)
	stop, err := p.Start()
	if err != nil {
		t.Fatal(err)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
}

func TestMakeTmpDir(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.pmtiles")

	tmp, cleanup, err := MakeTmpDir("", out, ".tool-tmp-")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(tmp) != dir {
		t.Errorf("temp dir %s is not next to the output", tmp)
	}
	if err := os.WriteFile(filepath.Join(tmp, "tiles.tmp"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out+".partial", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cleanup()
	for _, p := range []string{tmp, out + ".partial"} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s survived cleanup (%v)", p, err)
		}
	}
	cleanup() // idempotent

	// --tmp-dir elsewhere; the .partial still lives next to the output.
	other := t.TempDir()
	tmp, cleanup, err = MakeTmpDir(other, out, ".tool-tmp-")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if filepath.Dir(tmp) != other {
		t.Errorf("temp dir %s is not in --tmp-dir %s", tmp, other)
	}

	if _, _, err := MakeTmpDir(filepath.Join(dir, "missing"), out, ".tool-tmp-"); err == nil {
		t.Error("MakeTmpDir in a missing directory: want an error")
	}
}
