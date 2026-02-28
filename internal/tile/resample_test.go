package tile

import (
	"math"
	"testing"
)

// --- gamma LUTs ---

func TestBuildGammaLUTs_NilCases(t *testing.T) {
	// Disabled when gamma <= 0 or == 1.0.
	for _, g := range []float64{0, -1, 1.0} {
		if luts := buildGammaLUTs(g); luts != nil {
			t.Errorf("buildGammaLUTs(%v) = non-nil, want nil", g)
		}
	}
}

func TestBuildGammaLUTs_NonNil(t *testing.T) {
	luts := buildGammaLUTs(2.2)
	if luts == nil {
		t.Fatal("buildGammaLUTs(2.2) = nil, want non-nil")
	}
}

func TestGammaLUTs_EncodeBounds(t *testing.T) {
	luts := buildGammaLUTs(2.2)
	if luts == nil {
		t.Fatal("buildGammaLUTs(2.2) = nil")
	}
	// Endpoints must be exact.
	if v := luts.encode(0); v != 0 {
		t.Errorf("encode(0) = %d, want 0", v)
	}
	if v := luts.encode(255); v != 255 {
		t.Errorf("encode(255) = %d, want 255", v)
	}
	// Negative and overflow clamp.
	if v := luts.encode(-10); v != 0 {
		t.Errorf("encode(-10) = %d, want 0", v)
	}
	if v := luts.encode(300); v != 255 {
		t.Errorf("encode(300) = %d, want 255", v)
	}
}

func TestGammaLUTs_EncodeBrightensMidtones(t *testing.T) {
	// With gamma > 1, encode(v) = v^(1/gamma)*255 should produce
	// brighter (higher) output for midtone inputs.
	luts := buildGammaLUTs(2.2)
	if luts == nil {
		t.Fatal("buildGammaLUTs(2.2) = nil")
	}
	mid := luts.encode(128)
	if mid <= 128 {
		t.Errorf("encode(128) = %d, want > 128 (gamma encode should brighten midtones)", mid)
	}
}

func TestGammaLUTs_EncodeMonotonic(t *testing.T) {
	luts := buildGammaLUTs(2.2)
	if luts == nil {
		t.Fatal("buildGammaLUTs(2.2) = nil")
	}
	// The encode table must be monotonically non-decreasing.
	for i := 1; i < gammaEncodeSize; i++ {
		if luts.toGamma[i] < luts.toGamma[i-1] {
			t.Fatalf("toGamma[%d]=%d < toGamma[%d]=%d: not monotonic",
				i, luts.toGamma[i], i-1, luts.toGamma[i-1])
		}
	}
}

// --- clamp ---

func TestClamp(t *testing.T) {
	tests := []struct{ v, lo, hi, want int }{
		{5, 0, 10, 5},       // in range
		{-1, 0, 10, 0},      // below lo
		{11, 0, 10, 10},     // above hi
		{0, 0, 10, 0},       // at lo
		{10, 0, 10, 10},     // at hi
		{0, 0, 0, 0},        // lo == hi
		{-5, -10, -1, -5},   // negative range, in range
		{-15, -10, -1, -10}, // below negative lo
	}
	for _, tt := range tests {
		got := clamp(tt.v, tt.lo, tt.hi)
		if got != tt.want {
			t.Errorf("clamp(%d, %d, %d) = %d, want %d", tt.v, tt.lo, tt.hi, got, tt.want)
		}
	}
}

// --- clampByte ---

func TestClampByte(t *testing.T) {
	tests := []struct {
		v    float64
		want uint8
	}{
		{0, 0},
		{255, 255},
		{127.4, 127}, // rounds down
		{127.5, 128}, // rounds up at 0.5
		{-1, 0},      // below 0
		{-100, 0},    // well below 0
		{256, 255},   // above 255
		{300, 255},   // well above 255
		{128.0, 128}, // exact integer
		{0.4, 0},     // rounds to 0
		{0.5, 1},     // rounds to 1
		{254.6, 255}, // rounds to 255
	}
	for _, tt := range tests {
		got := clampByte(tt.v)
		if got != tt.want {
			t.Errorf("clampByte(%v) = %d, want %d", tt.v, got, tt.want)
		}
	}
}

// --- lanczos3 kernel ---

func TestLanczos3_AtZero(t *testing.T) {
	if got := lanczos3(0); got != 1 {
		t.Errorf("lanczos3(0) = %v, want 1", got)
	}
}

func TestLanczos3_ZeroesAtIntegerOffsets(t *testing.T) {
	// At non-zero integer values within [-3,3], sin(k·π) ≈ 0.
	for _, x := range []float64{1, 2, -1, -2} {
		got := lanczos3(x)
		if math.Abs(got) > 1e-10 {
			t.Errorf("lanczos3(%v) = %v, want ~0", x, got)
		}
	}
}

func TestLanczos3_ZeroOutsideSupport(t *testing.T) {
	for _, x := range []float64{3.1, -3.1, 4, -10, 100} {
		got := lanczos3(x)
		if got != 0 {
			t.Errorf("lanczos3(%v) = %v, want 0 (outside support)", x, got)
		}
	}
}

func TestLanczos3_Symmetry(t *testing.T) {
	for _, x := range []float64{0.5, 1.0, 1.5, 2.0, 2.5, 0.1, 2.9} {
		pos := lanczos3(x)
		neg := lanczos3(-x)
		if math.Abs(pos-neg) > 1e-15 {
			t.Errorf("lanczos3 not symmetric at x=%v: pos=%v neg=%v", x, pos, neg)
		}
	}
}

func TestLanczos3_PositiveAtCenter(t *testing.T) {
	// The kernel is positive near the center (|x| < 1).
	for _, x := range []float64{0.1, 0.5, 0.9} {
		got := lanczos3(x)
		if got <= 0 {
			t.Errorf("lanczos3(%v) = %v, want > 0 near center", x, got)
		}
	}
}

// --- lanczos3LUT vs direct ---

func TestLanczos3LUT_AccuracyVsDirect(t *testing.T) {
	const maxErr = 0.001
	for i := 0; i <= 30; i++ {
		x := float64(i) * 0.1
		direct := lanczos3(x)
		lut := lanczos3LUT(x)
		if math.Abs(direct-lut) > maxErr {
			t.Errorf("lanczos3LUT(%v): direct=%v lut=%v diff=%v (max %v)",
				x, direct, lut, math.Abs(direct-lut), maxErr)
		}
	}
}

func TestLanczos3LUT_NegativeInputs(t *testing.T) {
	const maxErr = 0.001
	for i := 1; i <= 15; i++ {
		x := float64(i) * 0.2
		direct := lanczos3(-x)
		lut := lanczos3LUT(-x)
		if math.Abs(direct-lut) > maxErr {
			t.Errorf("lanczos3LUT(-%v): direct=%v lut=%v", x, direct, lut)
		}
	}
}

func TestLanczos3LUT_OutsideSupport(t *testing.T) {
	for _, x := range []float64{3.0, 3.1, -3.0, -3.1, 10} {
		got := lanczos3LUT(x)
		if got != 0 {
			t.Errorf("lanczos3LUT(%v) = %v, want 0", x, got)
		}
	}
}

func TestLanczos3LUT_AtZero(t *testing.T) {
	got := lanczos3LUT(0)
	if math.Abs(got-1) > 0.001 {
		t.Errorf("lanczos3LUT(0) = %v, want ~1", got)
	}
}

// --- bicubic kernel ---

func TestBicubic_AtZero(t *testing.T) {
	// bicubic(0) = 1.5*0 - 2.5*0 + 1 = 1.
	if got := bicubic(0); got != 1 {
		t.Errorf("bicubic(0) = %v, want 1", got)
	}
}

func TestBicubic_ZeroAtBoundary(t *testing.T) {
	// bicubic(1) = 1.5 - 2.5 + 1 = 0.
	if got := bicubic(1); math.Abs(got) > 1e-14 {
		t.Errorf("bicubic(1) = %v, want 0", got)
	}
	// bicubic(2) = -0.5*8 + 2.5*4 - 4*2 + 2 = -4+10-8+2 = 0.
	if got := bicubic(2); math.Abs(got) > 1e-14 {
		t.Errorf("bicubic(2) = %v, want 0", got)
	}
	// bicubic(-1) and bicubic(-2) are the same by symmetry.
	if got := bicubic(-1); math.Abs(got) > 1e-14 {
		t.Errorf("bicubic(-1) = %v, want 0", got)
	}
}

func TestBicubic_ZeroOutsideSupport(t *testing.T) {
	for _, x := range []float64{2.1, 3, 10, -2.1, -3, -10} {
		got := bicubic(x)
		if got != 0 {
			t.Errorf("bicubic(%v) = %v, want 0 (outside support)", x, got)
		}
	}
}

func TestBicubic_Symmetry(t *testing.T) {
	for _, x := range []float64{0.5, 1.0, 1.5, 0.1, 1.9} {
		pos := bicubic(x)
		neg := bicubic(-x)
		if math.Abs(pos-neg) > 1e-15 {
			t.Errorf("bicubic not symmetric at x=%v: pos=%v neg=%v", x, pos, neg)
		}
	}
}

func TestBicubic_DecreaseInnerRegion(t *testing.T) {
	// The peak is at x=0 and the function decreases monotonically to x=1 in inner region.
	prev := bicubic(0.0)
	for i := 1; i <= 10; i++ {
		x := float64(i) * 0.1
		cur := bicubic(x)
		if cur > prev+1e-12 {
			t.Errorf("bicubic(%v)=%v > bicubic(%v)=%v; should decrease toward 1",
				x, cur, x-0.1, prev)
		}
		prev = cur
	}
}

// --- bicubicLUT vs direct ---

func TestBicubicLUT_AccuracyVsDirect(t *testing.T) {
	const maxErr = 0.001
	for i := 0; i <= 20; i++ {
		x := float64(i) * 0.1
		direct := bicubic(x)
		lut := bicubicLUT(x)
		if math.Abs(direct-lut) > maxErr {
			t.Errorf("bicubicLUT(%v): direct=%v lut=%v diff=%v (max %v)",
				x, direct, lut, math.Abs(direct-lut), maxErr)
		}
	}
}

func TestBicubicLUT_NegativeInputs(t *testing.T) {
	const maxErr = 0.001
	for i := 1; i <= 10; i++ {
		x := float64(i) * 0.15
		direct := bicubic(-x)
		lut := bicubicLUT(-x)
		if math.Abs(direct-lut) > maxErr {
			t.Errorf("bicubicLUT(-%v): direct=%v lut=%v", x, direct, lut)
		}
	}
}

func TestBicubicLUT_OutsideSupport(t *testing.T) {
	for _, x := range []float64{2.0, 2.5, -2.0, -2.5, 10} {
		got := bicubicLUT(x)
		if got != 0 {
			t.Errorf("bicubicLUT(%v) = %v, want 0", x, got)
		}
	}
}

func TestBicubicLUT_AtZero(t *testing.T) {
	got := bicubicLUT(0)
	if math.Abs(got-1) > 0.001 {
		t.Errorf("bicubicLUT(0) = %v, want ~1", got)
	}
}

// --- ParseResampling ---

func TestParseResampling_ValidInputs(t *testing.T) {
	tests := []struct {
		input string
		want  Resampling
	}{
		{"lanczos", ResamplingLanczos},
		{"bicubic", ResamplingBicubic},
		{"bilinear", ResamplingBilinear},
		{"nearest", ResamplingNearest},
		{"mode", ResamplingMode},
	}
	for _, tt := range tests {
		got, err := ParseResampling(tt.input)
		if err != nil {
			t.Errorf("ParseResampling(%q): unexpected error: %v", tt.input, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseResampling(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestParseResampling_InvalidInputs(t *testing.T) {
	invalids := []string{
		"",
		"Lanczos",
		"BILINEAR",
		"cubic",
		"linear",
		"average",
		" bilinear",
		"bilinear ",
	}
	for _, s := range invalids {
		_, err := ParseResampling(s)
		if err == nil {
			t.Errorf("ParseResampling(%q): expected error, got nil", s)
		}
	}
}

func TestParseResampling_AllModesDistinct(t *testing.T) {
	modes := []string{"lanczos", "bicubic", "bilinear", "nearest", "mode"}
	seen := make(map[Resampling]string)
	for _, m := range modes {
		r, err := ParseResampling(m)
		if err != nil {
			t.Fatalf("ParseResampling(%q) failed: %v", m, err)
		}
		if prev, ok := seen[r]; ok {
			t.Errorf("ParseResampling(%q) and ParseResampling(%q) both return %v", m, prev, r)
		}
		seen[r] = m
	}
}
