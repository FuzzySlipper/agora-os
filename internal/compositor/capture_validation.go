package compositor

import (
	"bytes"
	"image/color"
	"image/png"

	"github.com/patch/agora-os/internal/schema"
)

func inspectCapturePNG(data []byte) (*schema.ArtifactVisualInspection, error) {
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	bounds := img.Bounds()
	inspection := &schema.ArtifactVisualInspection{
		Status:  "visible",
		Width:   bounds.Dx(),
		Height:  bounds.Dy(),
		Mode:    colorModelName(img.ColorModel()),
		Extrema: [][2]uint8{{255, 0}, {255, 0}, {255, 0}, {255, 0}},
	}
	if bounds.Empty() {
		inspection.Status = "blank"
		inspection.Classification = "empty-png"
		inspection.Extrema = [][2]uint8{{0, 0}, {0, 0}, {0, 0}, {0, 0}}
		return inspection, nil
	}

	unique := make(map[color.RGBA]struct{}, 17)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r16, g16, b16, a16 := img.At(x, y).RGBA()
			channels := [4]uint8{uint8(r16 >> 8), uint8(g16 >> 8), uint8(b16 >> 8), uint8(a16 >> 8)}
			for i, value := range channels {
				if value < inspection.Extrema[i][0] {
					inspection.Extrema[i][0] = value
				}
				if value > inspection.Extrema[i][1] {
					inspection.Extrema[i][1] = value
				}
			}
			if len(unique) <= 16 {
				unique[color.RGBA{R: channels[0], G: channels[1], B: channels[2], A: channels[3]}] = struct{}{}
			}
		}
	}
	inspection.UniqueColorsSampled = len(unique)
	if inspection.Extrema[3][1] == 0 || (inspection.Extrema[0][1] == 0 && inspection.Extrema[1][1] == 0 && inspection.Extrema[2][1] == 0 && inspection.Extrema[3][1] == 0) {
		inspection.Status = "blank"
		inspection.Classification = "blank-or-transparent-png"
	}
	return inspection, nil
}

func colorModelName(model color.Model) string {
	switch model {
	case color.RGBAModel:
		return "RGBA"
	case color.NRGBAModel:
		return "NRGBA"
	case color.AlphaModel:
		return "Alpha"
	case color.GrayModel:
		return "Gray"
	default:
		return "image"
	}
}
