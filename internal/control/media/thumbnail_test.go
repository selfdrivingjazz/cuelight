package media

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func TestEncodeThumbnailFitsLongestEdgeAndPreservesContent(t *testing.T) {
	cases := []struct{ w, h, wantW, wantH int }{
		{1600, 400, 640, 160},
		{300, 1200, 160, 640},
		{120, 80, 120, 80}, // never upscales
	}
	for _, tc := range cases {
		source := image.NewRGBA(image.Rect(0, 0, tc.w, tc.h))
		for y := range tc.h {
			for x := range tc.w {
				source.Set(x, y, color.RGBA{200, 40, 10, 255})
			}
		}
		var encoded bytes.Buffer
		if err := png.Encode(&encoded, source); err != nil {
			t.Fatal(err)
		}
		out, w, h, err := EncodeThumbnail(encoded.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		if w != tc.wantW || h != tc.wantH {
			t.Fatalf("%dx%d -> %dx%d, want %dx%d", tc.w, tc.h, w, h, tc.wantW, tc.wantH)
		}
		decoded, err := jpeg.Decode(bytes.NewReader(out))
		if err != nil {
			t.Fatal(err)
		}
		r, _, _, _ := decoded.At(w/2, h/2).RGBA()
		if r>>8 < 180 {
			t.Fatalf("thumbnail lost source colour: r=%d", r>>8)
		}
	}
}

func TestEncodeThumbnailRejectsUndecodableSource(t *testing.T) {
	if _, _, _, err := EncodeThumbnail([]byte("\x00\x00\x00\x18ftypmp42 not an image")); !errors.Is(err, ErrThumbnailUnsupported) {
		t.Fatalf("err = %v, want ErrThumbnailUnsupported", err)
	}
}
