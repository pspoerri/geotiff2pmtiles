package tile

import (
	"image"
	"sync"
)

// rgbaPoolKey identifies a pool by image dimensions.
type rgbaPoolKey struct {
	w, h int
}

// rgbaPools maps (width, height) → *sync.Pool of *image.RGBA.
// Using sync.Map avoids a mutex on the hot path; in practice only 1-2
// distinct tile sizes exist per run, so the map stays tiny.
var rgbaPools sync.Map

// GetRGBA returns a zeroed *image.RGBA from the pool, or allocates a new one.
// The returned image has Rect (0,0)-(w,h) with all pixels set to zero.
func GetRGBA(w, h int) *image.RGBA {
	key := rgbaPoolKey{w, h}
	if p, ok := rgbaPools.Load(key); ok {
		if v := p.(*sync.Pool).Get(); v != nil {
			img := v.(*image.RGBA)
			clear(img.Pix)
			return img
		}
	}
	return image.NewRGBA(image.Rect(0, 0, w, h))
}

// PutRGBA returns an *image.RGBA to the pool for reuse.
// Nil images are silently ignored.
func PutRGBA(img *image.RGBA) {
	if img == nil {
		return
	}
	key := rgbaPoolKey{img.Rect.Dx(), img.Rect.Dy()}
	p, _ := rgbaPools.LoadOrStore(key, &sync.Pool{})
	p.(*sync.Pool).Put(img)
}
