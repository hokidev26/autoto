package media

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math/rand"
	"strings"
	"testing"
)

func TestProcessImageReencodesPNGAndHashesOriginal(t *testing.T) {
	original := encodePNG(t, image.NewNRGBA(image.Rect(0, 0, 3, 2)))
	result := ProcessImage(original)
	if result.ProcessingStatus != ProcessingReady || result.ModelMIME != MIMEPNG || result.Width != 3 || result.Height != 2 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(result.ModelData) == 0 || len(result.ModelData) > MaxModelImageBytes || result.SHA256 == "" {
		t.Fatalf("expected bounded model data and SHA: %+v", result)
	}
	if &result.ModelData[0] == &original[0] {
		t.Fatal("model data must be an independent re-encoding")
	}
}

func TestProcessImageStripsJPEGMetadata(t *testing.T) {
	var encoded bytes.Buffer
	fixture := image.NewRGBA(image.Rect(0, 0, 4, 4))
	fixture.Set(0, 0, color.RGBA{R: 255, A: 255})
	if err := jpeg.Encode(&encoded, fixture, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	metadata := []byte("Exif\x00\x00TOP_SECRET_METADATA")
	segment := []byte{0xff, 0xe1, byte((len(metadata) + 2) >> 8), byte(len(metadata) + 2)}
	original := append([]byte{}, encoded.Bytes()[:2]...)
	original = append(original, segment...)
	original = append(original, metadata...)
	original = append(original, encoded.Bytes()[2:]...)

	result := ProcessImage(original)
	if result.ProcessingStatus != ProcessingReady || result.ModelMIME != MIMEJPEG {
		t.Fatalf("unexpected result: %+v", result)
	}
	if bytes.Contains(result.ModelData, []byte("TOP_SECRET_METADATA")) {
		t.Fatal("normalized JPEG retained metadata")
	}
}

func TestProcessImageGIFUsesFirstFrame(t *testing.T) {
	palette := color.Palette{color.RGBA{R: 255, A: 255}, color.RGBA{B: 255, A: 255}}
	first := image.NewPaletted(image.Rect(0, 0, 1, 1), palette)
	first.SetColorIndex(0, 0, 0)
	second := image.NewPaletted(image.Rect(0, 0, 1, 1), palette)
	second.SetColorIndex(0, 0, 1)
	var encoded bytes.Buffer
	if err := gif.EncodeAll(&encoded, &gif.GIF{Image: []*image.Paletted{first, second}, Delay: []int{0, 0}}); err != nil {
		t.Fatal(err)
	}

	result := ProcessImage(encoded.Bytes())
	if result.ProcessingStatus != ProcessingReady || result.ModelMIME != MIMEPNG {
		t.Fatalf("unexpected result: %+v", result)
	}
	decoded, err := png.Decode(bytes.NewReader(result.ModelData))
	if err != nil {
		t.Fatal(err)
	}
	r, _, b, _ := decoded.At(0, 0).RGBA()
	if r <= b {
		t.Fatalf("expected first red frame, got red=%d blue=%d", r, b)
	}
}

func TestProcessImageRejectsCorruptAndOverBudgetImages(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		code string
	}{
		{name: "unsupported", data: []byte("not an image"), code: CodeUnsupported},
		{name: "corrupt PNG", data: append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, []byte("broken")...), code: CodeInvalid},
		{name: "edge", data: pngHeader(8193, 1, 8, 6), code: CodeDimensions},
		{name: "pixels", data: pngHeader(8000, 5000, 8, 6), code: CodePixels},
		{name: "decode budget", data: pngHeader(8000, 4000, 16, 6), code: CodeDecodeBudget},
		{name: "transparent grayscale worst-case decode budget", data: pngHeader(7000, 4000, 16, 0), code: CodeDecodeBudget},
		{name: "input budget", data: append(append([]byte{}, pngHeader(1, 1, 8, 6)...), make([]byte, MaxImageInputBytes)...), code: CodeInputBudget},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := ProcessImage(test.data)
			if result.ProcessingStatus != ProcessingRejected || result.ProcessingCode != test.code || result.ProcessingError == "" || len(result.ModelData) != 0 {
				t.Fatalf("unexpected rejection: %+v", result)
			}
		})
	}
}

func TestProcessImageShrinksNoisyPNGToModelBudget(t *testing.T) {
	fixture := image.NewNRGBA(image.Rect(0, 0, 1400, 1400))
	random := rand.New(rand.NewSource(7))
	if _, err := random.Read(fixture.Pix); err != nil {
		t.Fatal(err)
	}
	original := encodePNG(t, fixture)
	if len(original) <= MaxModelImageBytes || int64(len(original)) > MaxImageInputBytes {
		t.Fatalf("fixture must exceed model budget but fit input budget: %d", len(original))
	}
	result := ProcessImage(original)
	if result.ProcessingStatus != ProcessingReady || len(result.ModelData) == 0 || len(result.ModelData) > MaxModelImageBytes {
		t.Fatalf("unexpected result: status=%s code=%s bytes=%d", result.ProcessingStatus, result.ProcessingCode, len(result.ModelData))
	}
	if result.Width >= fixture.Bounds().Dx() || result.Height >= fixture.Bounds().Dy() {
		t.Fatalf("expected oversized output to be resized, got %dx%d", result.Width, result.Height)
	}
}

func encodePNG(t *testing.T, image image.Image) []byte {
	t.Helper()
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func pngHeader(width, height uint32, bitDepth, colorType byte) []byte {
	var output bytes.Buffer
	output.Write([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], width)
	binary.BigEndian.PutUint32(ihdr[4:8], height)
	ihdr[8] = bitDepth
	ihdr[9] = colorType
	writePNGChunk(&output, "IHDR", ihdr)
	writePNGChunk(&output, "IEND", nil)
	return output.Bytes()
}

func writePNGChunk(output *bytes.Buffer, kind string, data []byte) {
	_ = binary.Write(output, binary.BigEndian, uint32(len(data)))
	output.WriteString(kind)
	output.Write(data)
	checksum := crc32.ChecksumIEEE(append([]byte(kind), data...))
	_ = binary.Write(output, binary.BigEndian, checksum)
}

func TestProcessingErrorsAreSafeAndSpecific(t *testing.T) {
	result := ProcessImage([]byte("unknown"))
	if !strings.Contains(result.ProcessingError, "PNG、JPEG 或 GIF") || strings.Contains(result.ProcessingError, "unknown") {
		t.Fatalf("unexpected public processing error: %q", result.ProcessingError)
	}
}
