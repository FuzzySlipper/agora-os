package compositor

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func TestInspectCapturePNGClassifiesTransparentBlank(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	inspection, err := inspectCapturePNG(buf.Bytes())
	if err != nil {
		t.Fatalf("inspectCapturePNG: %v", err)
	}
	if inspection.Status != "blank" || inspection.Classification != "blank-or-transparent-png" {
		t.Fatalf("got inspection %+v, want blank transparent classification", inspection)
	}
}

func TestInspectCapturePNGClassifiesVisible(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.SetRGBA(1, 1, color.RGBA{R: 255, G: 20, B: 30, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	inspection, err := inspectCapturePNG(buf.Bytes())
	if err != nil {
		t.Fatalf("inspectCapturePNG: %v", err)
	}
	if inspection.Status != "visible" {
		t.Fatalf("got inspection %+v, want visible", inspection)
	}
}
