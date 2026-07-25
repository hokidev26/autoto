package media

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
)

const (
	MaxImageInputBytes int64 = 10 << 20
	MaxImageDimension        = 8192
	MaxImagePixels     int64 = 32_000_000
	MaxDecodedBytes    int64 = 192 << 20
	MaxModelImageBytes       = 4 << 20

	ProcessingReady    = "ready"
	ProcessingRejected = "rejected"

	MIMEPNG  = "image/png"
	MIMEJPEG = "image/jpeg"
	MIMEGIF  = "image/gif"
)

const (
	CodeUnsupported  = "unsupported_format"
	CodeInputBudget  = "input_budget_exceeded"
	CodeInvalid      = "invalid_image"
	CodeDimensions   = "dimensions_exceeded"
	CodePixels       = "pixels_exceeded"
	CodeDecodeBudget = "decode_budget_exceeded"
	CodeOutputBudget = "output_budget_exceeded"
)

// ImageResult contains the server-authoritative model representation of an
// uploaded image. Callers retain the original bytes separately.
type ImageResult struct {
	ModelData        []byte
	ModelMIME        string
	Width            int
	Height           int
	SHA256           string
	ProcessingStatus string
	ProcessingCode   string
	ProcessingError  string
}

// ProcessImage verifies PNG/JPEG/GIF bytes, bounds decoding work, decodes only
// the first GIF frame, and emits a metadata-free model image within 4 MiB.
func ProcessImage(data []byte) ImageResult {
	sum := sha256.Sum256(data)
	result := ImageResult{SHA256: hex.EncodeToString(sum[:]), ProcessingStatus: ProcessingRejected}
	format, _ := detectImageFormat(data)
	if format == "" {
		return reject(result, CodeUnsupported, "不支持的图片格式；仅接受 PNG、JPEG 或 GIF。")
	}
	if int64(len(data)) > MaxImageInputBytes {
		return reject(result, CodeInputBudget, "图片数据超过 10 MiB 处理预算。")
	}

	config, err := decodeConfig(format, data)
	if err != nil || config.Width <= 0 || config.Height <= 0 {
		return reject(result, CodeInvalid, "图片字节损坏或格式无效。")
	}
	if config.Width > MaxImageDimension || config.Height > MaxImageDimension {
		return reject(result, CodeDimensions, "图片尺寸超过 8192 像素边长限制。")
	}
	pixels := int64(config.Width) * int64(config.Height)
	if pixels <= 0 || pixels > MaxImagePixels {
		return reject(result, CodePixels, "图片像素数超过 32000000 限制。")
	}
	if pixels*estimatedDecodedBytesPerPixel(format, data) > MaxDecodedBytes {
		return reject(result, CodeDecodeBudget, "图片解码内存预算超限。")
	}

	decoded, err := decodeImage(format, data)
	if err != nil {
		return reject(result, CodeInvalid, "图片字节损坏或格式无效。")
	}
	bounds := decoded.Bounds()
	if bounds.Dx() != config.Width || bounds.Dy() != config.Height {
		return reject(result, CodeInvalid, "图片解码尺寸与文件头不一致。")
	}
	modelData, modelMIME, width, height, err := encodeModelImage(decoded, format)
	if err != nil {
		return reject(result, CodeOutputBudget, "图片无法在 4 MiB 模型输入预算内规范化。")
	}
	result.ModelData = modelData
	result.ModelMIME = modelMIME
	result.Width = width
	result.Height = height
	result.ProcessingStatus = ProcessingReady
	result.ProcessingCode = ""
	result.ProcessingError = ""
	return result
}

func DetectImageMIME(data []byte) string {
	_, mimeType := detectImageFormat(data)
	return mimeType
}

func IsSupportedImageMIME(mimeType string) bool {
	return mimeType == MIMEPNG || mimeType == MIMEJPEG || mimeType == MIMEGIF
}

func IsModelImageMIME(mimeType string) bool {
	return mimeType == MIMEPNG || mimeType == MIMEJPEG
}

func reject(result ImageResult, code, message string) ImageResult {
	result.ProcessingCode = code
	result.ProcessingError = message
	return result
}

func detectImageFormat(data []byte) (string, string) {
	switch {
	case len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}):
		return "png", MIMEPNG
	case len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff:
		return "jpeg", MIMEJPEG
	case len(data) >= 6 && (bytes.Equal(data[:6], []byte("GIF87a")) || bytes.Equal(data[:6], []byte("GIF89a"))):
		return "gif", MIMEGIF
	default:
		return "", ""
	}
}

func decodeConfig(format string, data []byte) (image.Config, error) {
	reader := bytes.NewReader(data)
	switch format {
	case "png":
		return png.DecodeConfig(reader)
	case "jpeg":
		return jpeg.DecodeConfig(reader)
	case "gif":
		return gif.DecodeConfig(reader)
	default:
		return image.Config{}, errors.New("unsupported image format")
	}
}

func decodeImage(format string, data []byte) (image.Image, error) {
	reader := bytes.NewReader(data)
	switch format {
	case "png":
		return png.Decode(reader)
	case "jpeg":
		return jpeg.Decode(reader)
	case "gif":
		// gif.Decode intentionally decodes only the first frame.
		return gif.Decode(reader)
	default:
		return nil, errors.New("unsupported image format")
	}
}

func estimatedDecodedBytesPerPixel(format string, _ []byte) int64 {
	switch format {
	case "jpeg":
		return 2
	case "gif":
		return 1
	case "png":
		// Go's PNG decoder may expand grayscale or paletted input with tRNS
		// transparency into NRGBA64. Budget against that worst-case allocation
		// instead of trusting the compact IHDR color model.
		return 8
	default:
		return 8
	}
}

func encodeModelImage(source image.Image, sourceFormat string) ([]byte, string, int, int, error) {
	current := source
	for attempt := 0; attempt < 12; attempt++ {
		if sourceFormat == "jpeg" {
			for _, quality := range []int{85, 70, 55, 40} {
				data, overflow, err := encodeWithBudget(func(writer io.Writer) error {
					return jpeg.Encode(writer, current, &jpeg.Options{Quality: quality})
				})
				if err == nil && !overflow {
					bounds := current.Bounds()
					return data, MIMEJPEG, bounds.Dx(), bounds.Dy(), nil
				}
				if err != nil && !overflow {
					return nil, "", 0, 0, err
				}
			}
		} else {
			data, overflow, err := encodeWithBudget(func(writer io.Writer) error {
				return png.Encode(writer, current)
			})
			if err == nil && !overflow {
				bounds := current.Bounds()
				return data, MIMEPNG, bounds.Dx(), bounds.Dy(), nil
			}
			if err != nil && !overflow {
				return nil, "", 0, 0, err
			}
		}

		bounds := current.Bounds()
		width, height := bounds.Dx(), bounds.Dy()
		if width == 1 && height == 1 {
			break
		}
		newWidth := max(1, width*3/4)
		newHeight := max(1, height*3/4)
		if newWidth == width && width > 1 {
			newWidth--
		}
		if newHeight == height && height > 1 {
			newHeight--
		}
		current = resizeNearest(current, newWidth, newHeight)
	}
	return nil, "", 0, 0, errors.New("model image output budget exceeded")
}

type budgetWriter struct {
	buffer    bytes.Buffer
	remaining int
	overflow  bool
}

func (writer *budgetWriter) Write(data []byte) (int, error) {
	if len(data) <= writer.remaining {
		written, err := writer.buffer.Write(data)
		writer.remaining -= written
		return written, err
	}
	if writer.remaining > 0 {
		_, _ = writer.buffer.Write(data[:writer.remaining])
	}
	writer.remaining = 0
	writer.overflow = true
	return len(data), nil
}

func encodeWithBudget(encode func(io.Writer) error) ([]byte, bool, error) {
	writer := &budgetWriter{remaining: MaxModelImageBytes}
	err := encode(writer)
	if writer.overflow {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	return append([]byte(nil), writer.buffer.Bytes()...), false, nil
}

func resizeNearest(source image.Image, width, height int) *image.NRGBA {
	destination := image.NewNRGBA(image.Rect(0, 0, width, height))
	bounds := source.Bounds()
	sourceWidth, sourceHeight := bounds.Dx(), bounds.Dy()
	for y := 0; y < height; y++ {
		sourceY := bounds.Min.Y + y*sourceHeight/height
		for x := 0; x < width; x++ {
			sourceX := bounds.Min.X + x*sourceWidth/width
			destination.Set(x, y, source.At(sourceX, sourceY))
		}
	}
	return destination
}
