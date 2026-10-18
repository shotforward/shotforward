package mediafiles

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
)

// Strict validation decodes compressed images, so bound decoded memory as well
// as the existing byte-size limit. This is not a requested output-size limit.
const maximumDecodedImagePixels int64 = 64 << 20

func stagedImageExtension(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("%w: open staged image: %v", projectapp.ErrMediaImportUnreadable, err)
	}
	defer file.Close()
	var header [512]byte
	n, err := io.ReadFull(file, header[:])
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", fmt.Errorf("%w: inspect staged image: %v", projectapp.ErrMediaImportUnreadable, err)
	}
	switch http.DetectContentType(header[:n]) {
	case "image/png":
		return ".png", nil
	case "image/jpeg":
		return ".jpg", nil
	case "image/webp":
		return ".webp", nil
	default:
		return "", fmt.Errorf("%w: image content must be PNG, JPEG, or WebP", projectapp.ErrMediaImportUnsupported)
	}
}

func normalizeStagedImagePath(path, extension string) (string, error) {
	file, err := os.CreateTemp(filepath.Dir(path), "import-*"+extension)
	if err != nil {
		return path, fmt.Errorf("%w: create normalized image: %v", projectapp.ErrMediaImportUnreadable, err)
	}
	normalized := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(normalized)
		return path, fmt.Errorf("%w: close normalized image: %v", projectapp.ErrMediaImportUnreadable, err)
	}
	if err := os.Rename(path, normalized); err != nil {
		_ = os.Remove(normalized)
		return path, fmt.Errorf("%w: normalize image extension: %v", projectapp.ErrMediaImportUnreadable, err)
	}
	return normalized, nil
}

func (p Processor) validateStagedImage(ctx context.Context, path, extension string, width, height int) error {
	if err := validateDecodedImageDimensions(width, height); err != nil {
		return err
	}
	if extension == ".webp" {
		// Decode a single frame without re-encoding or writing a derived file.
		command := exec.CommandContext(ctx, p.ffmpeg, "-nostdin", "-v", "error", "-xerror", "-err_detect", "explode", "-threads", "1", "-max_pixels", strconv.FormatInt(maximumDecodedImagePixels, 10), "-i", path, "-map", "0:v:0", "-frames:v", "1", "-f", "null", "-")
		if output, err := command.CombinedOutput(); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("%w: decode WebP image: %v: %s", projectapp.ErrMediaImportInvalid, err, boundedMessage(output))
		}
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("%w: open staged image for validation: %v", projectapp.ErrMediaImportUnreadable, err)
	}
	defer file.Close()
	var decodeConfig func(io.Reader) (image.Config, error)
	var decode func(io.Reader) (image.Image, error)
	switch extension {
	case ".png":
		decodeConfig, decode = png.DecodeConfig, png.Decode
	case ".jpg", ".jpeg":
		decodeConfig, decode = jpeg.DecodeConfig, jpeg.Decode
	default:
		return projectapp.ErrMediaImportUnsupported
	}
	config, err := decodeConfig(&contextReader{ctx: ctx, reader: file})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: decode image configuration: %v", projectapp.ErrMediaImportInvalid, err)
	}
	// Do not trust ffprobe alone for allocation bounds: the actual decoder must
	// agree with the reported dimensions before it allocates pixel storage.
	if err := validateDecodedImageDimensions(config.Width, config.Height); err != nil {
		return err
	}
	if config.Width != width || config.Height != height {
		return fmt.Errorf("%w: decoded image dimensions differ from probe metadata", projectapp.ErrMediaImportInvalid)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("%w: rewind staged image: %v", projectapp.ErrMediaImportUnreadable, err)
	}
	if _, err := decode(&contextReader{ctx: ctx, reader: file}); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: decode image: %v", projectapp.ErrMediaImportInvalid, err)
	}
	return nil
}

func validateDecodedImageDimensions(width, height int) error {
	if width < 1 || height < 1 || int64(width) > maximumDecodedImagePixels/int64(height) {
		return fmt.Errorf("%w: image dimensions exceed 64 Mi pixels or are invalid", projectapp.ErrMediaImportInvalid)
	}
	return nil
}
