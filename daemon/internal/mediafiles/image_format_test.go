package mediafiles

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

func TestPrepareRecognizesRealImageContentAndPreservesBytesOnInstall(t *testing.T) {
	t.Parallel()
	processor := testProcessor(t)
	samples := imageFormatSamples(t)
	for _, test := range []struct {
		name, format, filename, extension, mime string
	}{
		{"JPEG named PNG", "jpeg", "tmp9tnyrb8o.png", ".jpg", "image/jpeg"},
		{"PNG named JPEG", "png", "reference.jpg", ".png", "image/png"},
		{"PNG without extension", "png", "reference", ".png", "image/png"},
		{"JPEG without extension", "jpeg", "reference", ".jpg", "image/jpeg"},
		{"PNG with unsupported extension", "png", "reference.gif", ".png", "image/png"},
		{"JPEG with unsupported extension", "jpeg", "reference.bin", ".jpg", "image/jpeg"},
		{"JPEG extension alias", "jpeg", "reference.jpeg", ".jpg", "image/jpeg"},
		{"JPEG uppercase extension", "jpeg", "reference.JPG", ".jpg", "image/jpeg"},
		{"WebP named PNG", "webp", "reference.png", ".webp", "image/webp"},
		{"WebP without extension", "webp", "reference", ".webp", "image/webp"},
		{"WebP proper extension", "webp", "reference.webp", ".webp", "image/webp"},
		{"PNG Unicode filename", "png", "一二的参考图.未知后缀", ".png", "image/png"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			source := filepath.Join(t.TempDir(), test.filename)
			original := samples[test.format]
			if err := os.WriteFile(source, original, 0o600); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(source)
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := processor.Prepare(context.Background(), projectapp.PrepareMediaRequest{
				ProjectRoot: root, SourcePath: source, OriginalFilename: test.filename, Kind: domain.MediaKindImage,
			})
			if err != nil {
				t.Fatal(err)
			}
			wantWidth, wantHeight := 16, 12
			if test.format == "webp" {
				wantWidth, wantHeight = 1, 1
			}
			if prepared.Extension != test.extension || filepath.Ext(prepared.AbsolutePath) != test.extension || prepared.MIMEType != test.mime || prepared.OriginalFilename != test.filename || prepared.Width != wantWidth || prepared.Height != wantHeight || prepared.DurationMilliseconds != 0 {
				t.Fatalf("normalized image metadata = %#v", prepared)
			}
			preparedBytes, err := os.ReadFile(prepared.AbsolutePath)
			hash := sha256.Sum256(original)
			if err != nil || !bytes.Equal(preparedBytes, original) || prepared.ByteSize != int64(len(original)) || prepared.SHA256 != hex.EncodeToString(hash[:]) {
				t.Fatalf("image bytes/hash changed: bytes=%d original=%d error=%v", len(preparedBytes), len(original), err)
			}
			id, _ := domain.ParseArtifactID("art_" + strings.Repeat("B", 26))
			installed, err := processor.Install(context.Background(), root, prepared, id)
			if err != nil || installed.RelativePath != "media/images/"+id.String()+test.extension {
				t.Fatalf("normalized installation = %#v, %v", installed, err)
			}
			installedBytes, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(installed.RelativePath)))
			if err != nil || !bytes.Equal(installedBytes, original) {
				t.Fatalf("installed image bytes changed: %v", err)
			}
			if _, err := os.Stat(prepared.AbsolutePath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("installed image retained staging copy: %v", err)
			}
			remaining, err := os.ReadFile(source)
			after, statErr := os.Stat(source)
			if err != nil || statErr != nil || !bytes.Equal(remaining, original) || !before.ModTime().Equal(after.ModTime()) {
				t.Fatalf("source image was modified: %v, %v", err, statErr)
			}
		})
	}
}

func TestPrepareRejectsInvalidOrUnsupportedRealImageContent(t *testing.T) {
	t.Parallel()
	processor := testProcessor(t)
	samples := imageFormatSamples(t)
	corruptPNG := append([]byte(nil), samples["png"]...)
	dataChunk := bytes.Index(corruptPNG, []byte("IDAT"))
	if dataChunk < 0 {
		t.Fatal("PNG fixture has no image data")
	}
	corruptPNG[dataChunk+4] ^= 0xff
	var gifBuffer bytes.Buffer
	if err := gif.Encode(&gifBuffer, image.NewRGBA(image.Rect(0, 0, 16, 12)), nil); err != nil {
		t.Fatal(err)
	}
	video := filepath.Join(t.TempDir(), "motion-jpeg.avi")
	runFFmpeg(t, processor.ffmpeg, "-f", "lavfi", "-i", "color=c=red:s=16x12:r=1", "-frames:v", "2", "-c:v", "mjpeg", video)
	videoBytes, err := os.ReadFile(video)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		data []byte
		err  error
	}{
		{"not an image", []byte("this is text, not an image"), projectapp.ErrMediaImportUnsupported},
		{"actual GIF named PNG", gifBuffer.Bytes(), projectapp.ErrMediaImportUnsupported},
		{"video container with JPEG codec", videoBytes, projectapp.ErrMediaImportUnsupported},
		{"PNG signature without image", []byte("\x89PNG\r\n\x1a\n"), projectapp.ErrMediaImportInvalid},
		{"PNG corrupt image data", corruptPNG, projectapp.ErrMediaImportInvalid},
		{"PNG truncated footer", samples["png"][:len(samples["png"])-12], projectapp.ErrMediaImportInvalid},
		{"JPEG truncated scan", samples["jpeg"][:len(samples["jpeg"])-30], projectapp.ErrMediaImportInvalid},
		{"JPEG missing EOI", samples["jpeg"][:len(samples["jpeg"])-2], projectapp.ErrMediaImportInvalid},
		{"WebP truncated image", samples["webp"][:len(samples["webp"])/2], projectapp.ErrMediaImportInvalid},
		{"WebP missing final byte", samples["webp"][:len(samples["webp"])-1], projectapp.ErrMediaImportInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			source := filepath.Join(t.TempDir(), "reference.png")
			if err := os.WriteFile(source, test.data, 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := processor.Prepare(context.Background(), projectapp.PrepareMediaRequest{ProjectRoot: root, SourcePath: source, Kind: domain.MediaKindImage})
			if !errors.Is(err, test.err) {
				t.Fatalf("invalid image error = %v, want %v", err, test.err)
			}
			assertNoStagedImages(t, root)
			actual, readErr := os.ReadFile(source)
			if readErr != nil || !bytes.Equal(actual, test.data) {
				t.Fatalf("invalid source was modified: %v", readErr)
			}
		})
	}
}

func TestPrepareKeepsTimedMediaFormatChecksStrict(t *testing.T) {
	t.Parallel()
	processor := testProcessor(t)
	video := filepath.Join(t.TempDir(), "source.mp4")
	audio := filepath.Join(t.TempDir(), "source.wav")
	runFFmpeg(t, processor.ffmpeg, "-f", "lavfi", "-i", "color=c=red:s=16x12:r=10", "-t", "0.2", "-c:v", "mpeg4", video)
	runFFmpeg(t, processor.ffmpeg, "-f", "lavfi", "-i", "anullsrc=r=8000:cl=mono", "-t", "0.2", audio)
	for _, test := range []struct {
		name, source, filename string
		kind                   domain.MediaKind
		err                    error
	}{
		{"MP4 named WebM", video, "reference.webm", domain.MediaKindVideo, projectapp.ErrMediaImportInvalid},
		{"WAV named MP3", audio, "reference.mp3", domain.MediaKindAudio, projectapp.ErrMediaImportInvalid},
		{"video without extension", video, "reference", domain.MediaKindVideo, projectapp.ErrMediaImportUnsupported},
		{"audio without extension", audio, "reference", domain.MediaKindAudio, projectapp.ErrMediaImportUnsupported},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(test.source)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), test.filename)
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			if _, err := processor.Prepare(context.Background(), projectapp.PrepareMediaRequest{ProjectRoot: root, SourcePath: path, Kind: test.kind}); !errors.Is(err, test.err) {
				t.Fatalf("timed media format error = %v", err)
			}
			assertNoStagedImages(t, root)
		})
	}
}

func TestStrictJPEGValidationRejectsTruncationThatProbeAccepts(t *testing.T) {
	t.Parallel()
	processor := testProcessor(t)
	data := imageFormatSamples(t)["jpeg"]
	path := filepath.Join(t.TempDir(), "missing-end-marker.jpg")
	if err := os.WriteFile(path, data[:len(data)-2], 0o600); err != nil {
		t.Fatal(err)
	}
	metadata, err := processor.probe(context.Background(), path, domain.MediaKindImage, ".jpg")
	if err != nil {
		t.Fatalf("fixture no longer demonstrates a successful metadata-only probe: %v", err)
	}
	err = processor.validateStagedImage(context.Background(), path, ".jpg", metadata.Width, metadata.Height)
	if !errors.Is(err, projectapp.ErrMediaImportInvalid) || !strings.Contains(err.Error(), "decode image") {
		t.Fatalf("truncated JPEG passed strict decoder: %v", err)
	}
}

func TestImageValidationChecksDecoderDimensionsBeforeAllocatingPixels(t *testing.T) {
	t.Parallel()
	const width, height = 100_000, 100_000
	var chunk bytes.Buffer
	chunk.WriteString("IHDR")
	var dimensions [8]byte
	binary.BigEndian.PutUint32(dimensions[:4], width)
	binary.BigEndian.PutUint32(dimensions[4:], height)
	chunk.Write(dimensions[:])
	chunk.Write([]byte{8, 2, 0, 0, 0}) // RGB, eight bits, no interlacing.
	var encoded bytes.Buffer
	encoded.WriteString("\x89PNG\r\n\x1a\n")
	if err := binary.Write(&encoded, binary.BigEndian, uint32(13)); err != nil {
		t.Fatal(err)
	}
	encoded.Write(chunk.Bytes())
	if err := binary.Write(&encoded, binary.BigEndian, crc32.ChecksumIEEE(chunk.Bytes())); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "huge.png")
	if err := os.WriteFile(path, encoded.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	// Simulate misleadingly small probe metadata; the decoder's actual header
	// dimensions must still be bounded without constructing a giant image.
	err := (Processor{}).validateStagedImage(context.Background(), path, ".png", 1, 1)
	if !errors.Is(err, projectapp.ErrMediaImportInvalid) || !strings.Contains(err.Error(), "64 Mi pixels") {
		t.Fatalf("decoder allocation guard = %v", err)
	}
	if err := validateDecodedImageDimensions(8192, 8192); err != nil {
		t.Fatalf("64 Mi pixel boundary rejected: %v", err)
	}
	for _, size := range [][2]int{{0, 1}, {1, 0}, {8192, 8193}, {int(^uint(0) >> 1), int(^uint(0) >> 1)}} {
		if err := validateDecodedImageDimensions(size[0], size[1]); !errors.Is(err, projectapp.ErrMediaImportInvalid) {
			t.Fatalf("oversized/invalid dimensions %v were accepted: %v", size, err)
		}
	}
}

func TestImageValidationKeepsSizeFilenameAndCancellationGuards(t *testing.T) {
	t.Parallel()
	processor := testProcessor(t)
	path := filepath.Join(t.TempDir(), "huge.png")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maximumImageBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := processor.Prepare(context.Background(), projectapp.PrepareMediaRequest{ProjectRoot: t.TempDir(), SourcePath: path, Kind: domain.MediaKindImage}); !errors.Is(err, projectapp.ErrMediaImportInvalid) {
		t.Fatalf("oversized image error = %v", err)
	}
	writePNG(t, path, 16, 12)
	for _, filename := range []string{"../reference.png", "folder\\reference.png", "reference\n.png"} {
		root := t.TempDir()
		if _, err := processor.Prepare(context.Background(), projectapp.PrepareMediaRequest{ProjectRoot: root, SourcePath: path, OriginalFilename: filename, Kind: domain.MediaKindImage}); !errors.Is(err, projectapp.ErrMediaImportInvalid) {
			t.Fatalf("unsafe original filename %q accepted: %v", filename, err)
		}
		assertNoStagedImages(t, root)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	root := t.TempDir()
	if _, err := processor.Prepare(ctx, projectapp.PrepareMediaRequest{ProjectRoot: root, SourcePath: path, Kind: domain.MediaKindImage}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled image preparation = %v", err)
	}
	assertNoStagedImages(t, root)
	if err := processor.validateStagedImage(ctx, path, ".png", 16, 12); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled image validation = %v", err)
	}
}

func imageFormatSamples(t *testing.T) map[string][]byte {
	t.Helper()
	directory := t.TempDir()
	pngPath := filepath.Join(directory, "source.png")
	writePNG(t, pngPath, 16, 12)
	pngData, err := os.ReadFile(pngPath)
	if err != nil {
		t.Fatal(err)
	}
	var jpegData bytes.Buffer
	value := image.NewRGBA(image.Rect(0, 0, 16, 12))
	for y := 0; y < 12; y++ {
		for x := 0; x < 16; x++ {
			value.SetRGBA(x, y, color.RGBA{R: uint8(x * 13), G: uint8(y * 19), B: 80, A: 255})
		}
	}
	if err := jpeg.Encode(&jpegData, value, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	// A valid one-pixel WebP fixture needs no encoder on test machines: the
	// bundled FFmpeg supports decoding WebP but does not include libwebp encoding.
	webpData, err := base64.StdEncoding.DecodeString("UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEADsD+JaQAA3AAAAAA")
	if err != nil {
		t.Fatal(err)
	}
	return map[string][]byte{"png": pngData, "jpeg": jpegData.Bytes(), "webp": webpData}
}

func writeTestGIF(t *testing.T, path string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := gif.Encode(file, image.NewRGBA(image.Rect(0, 0, 10, 10)), nil); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertNoStagedImages(t *testing.T, root string) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(stagingDirectory), "*"))
	if err != nil || len(paths) != 0 {
		t.Fatalf("failed image import retained staging files: %v, %v", paths, err)
	}
}
