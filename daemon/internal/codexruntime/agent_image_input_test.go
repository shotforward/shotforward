package codexruntime

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestAgentViewingImagesKeepSmallOriginals(t *testing.T) {
	for _, format := range []string{"jpeg", "png"} {
		t.Run(format, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(t.TempDir(), "original."+format)
			original := agentTestImage(t, path, image.Rect(0, 0, 321, 123), format, false)
			paths := []string{path}
			got, err := prepareAgentInputImages(context.Background(), root, paths)
			if err != nil || len(got) != 1 || got[0] != path || paths[0] != path {
				t.Fatalf("small original = %v, %v", got, err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(original, after) {
				t.Fatalf("original was changed: %v", err)
			}
			if _, err := os.Stat(filepath.Join(root, agentImageCacheDirectory)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("small original created a cache: %v", err)
			}
		})
	}
}

func TestAgentViewingImagePreservesOriginalAndAspectRatio(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(t.TempDir(), "source.png")
	original := agentTestImage(t, path, image.Rect(0, 0, 3000, 1200), "png", false)
	got, err := prepareAgentInputImages(context.Background(), root, []string{path})
	if err != nil || len(got) != 1 || got[0] == path {
		t.Fatalf("viewing copy = %v, %v", got, err)
	}
	view, encoded := agentTestReadImage(t, got[0])
	if bounds := view.Bounds(); bounds.Dx() != 2048 || bounds.Dy() != 819 {
		t.Fatalf("unexpected viewing dimensions: %v", bounds)
	}
	if len(encoded) > maximumAgentImageBytes || !bytes.HasPrefix(encoded, []byte{0xff, 0xd8}) {
		t.Fatalf("viewing copy is not a bounded JPEG: %d bytes", len(encoded))
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(original, after) {
		t.Fatalf("original was changed: %v", err)
	}
	if filepath.Dir(got[0]) != filepath.Join(root, agentImageCacheDirectory) {
		t.Fatalf("copy escaped the Runtime working directory: %s", got[0])
	}
	if runtime.GOOS != "windows" {
		for _, check := range []struct {
			path string
			mode os.FileMode
		}{
			{filepath.Dir(got[0]), 0o700}, {got[0], 0o600},
		} {
			info, err := os.Stat(check.path)
			if err != nil || info.Mode().Perm() != check.mode {
				t.Fatalf("cache permissions: %v, %v", info, err)
			}
		}
	}
}

func TestAgentViewingImageCompositesTransparencyOnWhite(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(t.TempDir(), "transparent.png")
	source := image.NewNRGBA(image.Rect(0, 0, 2600, 1300))
	for y := 0; y < source.Bounds().Dy(); y++ {
		for x := 0; x < source.Bounds().Dx(); x++ {
			if x < 1300 {
				source.SetNRGBA(x, y, color.NRGBA{R: 255, A: 128})
			}
		}
	}
	agentTestEncodeFile(t, path, source, "png")
	got, err := prepareAgentInputImages(context.Background(), root, []string{path})
	if err != nil {
		t.Fatal(err)
	}
	view, _ := agentTestReadImage(t, got[0])
	left := color.NRGBAModel.Convert(view.At(100, 100)).(color.NRGBA)
	right := color.NRGBAModel.Convert(view.At(view.Bounds().Dx()-100, 100)).(color.NRGBA)
	if left.R < 245 || left.G < 120 || left.G > 135 || left.B < 120 || left.B > 135 || left.A != 255 {
		t.Fatalf("half-transparent red was not blended over white: %+v", left)
	}
	if right.R < 250 || right.G < 250 || right.B < 250 || right.A != 255 {
		t.Fatalf("transparent area was not white: %+v", right)
	}
}

func TestAgentViewingImageNeverUpscalesAndHonorsByteBudget(t *testing.T) {
	// Incompressible pixels exercise quality and dimension fallback rather than
	// only the easily compressed diagrams used by the other tests.
	path := filepath.Join(t.TempDir(), "noise.png")
	original := agentTestImage(t, path, image.Rect(0, 0, 2048, 2048), "png", true)
	if len(original) <= maximumAgentImageBytes {
		t.Fatal("noise fixture is too small to exercise compression")
	}
	got, err := prepareAgentInputImages(context.Background(), t.TempDir(), []string{path})
	if err != nil {
		t.Fatal(err)
	}
	view, encoded := agentTestReadImage(t, got[0])
	if len(encoded) > maximumAgentImageBytes || view.Bounds().Dx() > 2048 || view.Bounds().Dy() > 2048 {
		t.Fatalf("viewing limit not honored: %d bytes, %v", len(encoded), view.Bounds())
	}
	if view.Bounds().Dx() == 2048 {
		t.Fatal("fixture did not exercise the dimension fallback after the quality floor")
	}
	small := image.NewRGBA(image.Rect(4, 7, 44, 27))
	resized, err := resizeAgentImage(context.Background(), small, maximumAgentImageEdge)
	if err != nil || resized.Bounds().Dx() != 40 || resized.Bounds().Dy() != 20 {
		t.Fatalf("small source was upscaled: %v, %v", resized, err)
	}
}

func TestAgentViewingImageReusesContentCacheAndIsolatesWorkingDirectories(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(t.TempDir(), "source.png")
	original := agentTestImage(t, path, image.Rect(0, 0, 2300, 1150), "png", false)
	first, err := prepareAgentInputImages(context.Background(), root, []string{path})
	if err != nil {
		t.Fatal(err)
	}
	firstInfo, err := os.Stat(first[0])
	if err != nil {
		t.Fatal(err)
	}
	duplicate := filepath.Join(t.TempDir(), "different-name.png")
	if err := os.WriteFile(duplicate, original, 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := prepareAgentInputImages(context.Background(), root, []string{duplicate})
	if err != nil || first[0] != second[0] {
		t.Fatalf("identical content did not reuse its cache: %v, %v", second, err)
	}
	secondInfo, err := os.Stat(second[0])
	if err != nil || !os.SameFile(firstInfo, secondInfo) || !firstInfo.ModTime().Equal(secondInfo.ModTime()) {
		t.Fatalf("cached copy was rewritten: %v", err)
	}
	otherRoot := t.TempDir()
	isolated, err := prepareAgentInputImages(context.Background(), otherRoot, []string{path})
	if err != nil || isolated[0] == first[0] || filepath.Dir(isolated[0]) != filepath.Join(otherRoot, agentImageCacheDirectory) {
		t.Fatalf("cache crossed working-directory boundary: %v, %v", isolated, err)
	}
	changed := image.NewRGBA(image.Rect(0, 0, 2300, 1150))
	agentTestEncodeFile(t, path, changed, "png")
	third, err := prepareAgentInputImages(context.Background(), root, []string{path})
	if err != nil || third[0] == first[0] {
		t.Fatalf("changed content reused an old cache entry: %v, %v", third, err)
	}
}

func TestAgentViewingImageConcurrentPreparationAndDamagedCacheRecovery(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(t.TempDir(), "source.png")
	agentTestImage(t, path, image.Rect(0, 0, 2400, 1200), "png", false)
	const callers = 6
	var group sync.WaitGroup
	var paths [callers]string
	var failures [callers]error
	start := make(chan struct{})
	for index := range callers {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			result, err := prepareAgentInputImages(context.Background(), root, []string{path})
			failures[index] = err
			if len(result) > 0 {
				paths[index] = result[0]
			}
		}()
	}
	close(start)
	group.Wait()
	for index := range callers {
		if failures[index] != nil || paths[index] != paths[0] || paths[index] == "" {
			t.Fatalf("concurrent result %d = %q, %v", index, paths[index], failures[index])
		}
	}
	entries, err := os.ReadDir(filepath.Join(root, agentImageCacheDirectory))
	if err != nil || len(entries) != 1 || strings.HasPrefix(entries[0].Name(), ".pending-") {
		t.Fatalf("cache contains partial or duplicate writes: %v, %v", entries, err)
	}
	if err := os.WriteFile(paths[0], []byte("damaged incomplete JPEG"), 0o600); err != nil {
		t.Fatal(err)
	}
	repaired, err := prepareAgentInputImages(context.Background(), root, []string{path})
	if err != nil || repaired[0] != paths[0] {
		t.Fatalf("damaged cache not repaired: %v, %v", repaired, err)
	}
	agentTestReadImage(t, repaired[0])
}

func TestAgentViewingImageRejectsUnsafeAndMalformedSources(t *testing.T) {
	root, sourceRoot := t.TempDir(), t.TempDir()
	empty := filepath.Join(sourceRoot, "empty.png")
	malformed := filepath.Join(sourceRoot, "malformed.png")
	truncated := filepath.Join(sourceRoot, "truncated.png")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(malformed, []byte("not an image"), 0o600); err != nil {
		t.Fatal(err)
	}
	encoded := agentTestImage(t, truncated, image.Rect(0, 0, 20, 10), "png", false)
	if err := os.WriteFile(truncated, encoded[:len(encoded)-12], 0o600); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{"relative": "relative.png", "missing": filepath.Join(sourceRoot, "missing.png"), "directory": sourceRoot, "empty": empty, "malformed": malformed, "truncated": truncated} {
		t.Run(name, func(t *testing.T) {
			if got, err := prepareAgentInputImages(context.Background(), root, []string{path}); err == nil || len(got) != 0 {
				t.Fatalf("unsafe source accepted: %v, %v", got, err)
			}
		})
	}
	for _, dimensions := range [][2]int{{10000, 10000}, {maximumAgentSourceEdge + 1, 1}, {0, 10}} {
		path := filepath.Join(sourceRoot, "dimensions.png")
		if err := os.WriteFile(path, agentTestPNGHeader(dimensions[0], dimensions[1]), 0o600); err != nil {
			t.Fatal(err)
		}
		if got, err := prepareAgentInputImages(context.Background(), root, []string{path}); err == nil || len(got) != 0 {
			t.Fatalf("abnormal dimensions accepted: %v, %v", got, err)
		}
	}
	oversized := filepath.Join(sourceRoot, "oversized.png")
	file, err := os.Create(oversized)
	if err != nil {
		t.Fatal(err)
	}
	truncateErr := file.Truncate(maximumAgentSourceBytes + 1)
	file.Close()
	if truncateErr != nil {
		t.Fatal(truncateErr)
	}
	if _, err := prepareAgentInputImages(context.Background(), root, []string{oversized}); err == nil {
		t.Fatal("oversized source accepted")
	}
}

func TestAgentViewingImageRejectsSymbolicLinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires Windows developer privileges")
	}
	path := filepath.Join(t.TempDir(), "source.png")
	agentTestImage(t, path, image.Rect(0, 0, 2300, 1150), "png", false)
	link := filepath.Join(t.TempDir(), "source-link.png")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareAgentInputImages(context.Background(), t.TempDir(), []string{link}); err == nil {
		t.Fatal("source symlink accepted")
	}
	root, elsewhere := t.TempDir(), t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(root, agentImageCacheDirectory)); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareAgentInputImages(context.Background(), root, []string{path}); err == nil {
		t.Fatal("cache-directory symlink accepted")
	}
	entries, err := os.ReadDir(elsewhere)
	if err != nil || len(entries) != 0 {
		t.Fatalf("cache wrote through directory symlink: %v, %v", entries, err)
	}
	root = t.TempDir()
	prepared, err := prepareAgentInputImages(context.Background(), root, []string{path})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(prepared[0]); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(elsewhere, "untouched.txt")
	if err := os.WriteFile(target, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, prepared[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareAgentInputImages(context.Background(), root, []string{path}); err == nil {
		t.Fatal("cache-entry symlink accepted")
	}
	content, err := os.ReadFile(target)
	if err != nil || string(content) != "untouched" {
		t.Fatalf("symlink target changed: %v", err)
	}
}

func TestAgentViewingImageCancellationDoesNotPublishPartialFiles(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := prepareAgentInputImages(ctx, root, []string{"/not-read.png"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("preparation ignored cancellation: %v", err)
	}
	if _, err := prepareAgentInputImages(nil, root, nil); err == nil {
		t.Fatal("nil context accepted")
	}
	if got, err := prepareAgentInputImages(context.Background(), "", nil); err != nil || len(got) != 0 {
		t.Fatalf("no-image request required a cache: %v, %v", got, err)
	}
	if _, err := resizeAgentImage(ctx, image.NewRGBA(image.Rect(0, 0, 100, 100)), 50); !errors.Is(err, context.Canceled) {
		t.Fatalf("resampler ignored cancellation: %v", err)
	}
	cache, err := openAgentImageCache(root)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	if err := writeAgentImageCache(ctx, cache, "cancelled.jpg", []byte("not published")); !errors.Is(err, context.Canceled) {
		t.Fatalf("writer ignored cancellation: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(root, agentImageCacheDirectory))
	if err != nil || len(entries) != 0 {
		t.Fatalf("cancellation left partial files: %v, %v", entries, err)
	}
	key := filepath.Join(root, "gate")
	release, err := acquireAgentImagePreparation(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	waiter, stopWaiting := context.WithCancel(context.Background())
	stopWaiting()
	if _, err := acquireAgentImagePreparation(waiter, key); !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting caller ignored cancellation: %v", err)
	}
	release()
	agentImagePreparationGates.Lock()
	_, leaked := agentImagePreparationGates.entries[key]
	agentImagePreparationGates.Unlock()
	if leaked {
		t.Fatal("cancelled cache waiter leaked its preparation gate")
	}
}

func TestAgentViewingWebPPreservesRuntimeCompatibility(t *testing.T) {
	// A small genuine WebP is passed through byte-for-byte; no new codec library
	// is needed for this already-supported Runtime format.
	encoded, err := base64.StdEncoding.DecodeString("UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEADsD+JaQAA3AAAAAA")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "small.webp")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := prepareAgentInputImages(context.Background(), t.TempDir(), []string{path})
	if err != nil || len(got) != 1 || got[0] != path {
		t.Fatalf("small WebP fallback = %v, %v", got, err)
	}
	// A larger complete RIFF container remains delegated to the Runtime. Add a
	// legal-sized opaque metadata chunk; this helper need not decode its pixels.
	large := bytes.Clone(encoded)
	large = append(large, []byte{'X', 'M', 'P', ' ', 0, 0, 0x10, 0}...)
	large = append(large, make([]byte, maximumAgentImageBytes)...)
	binary.LittleEndian.PutUint32(large[4:8], uint32(len(large)-8))
	if err := os.WriteFile(path, large, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = prepareAgentInputImages(context.Background(), t.TempDir(), []string{path})
	if err != nil || len(got) != 1 || got[0] != path {
		t.Fatalf("large WebP compatibility = %v, %v", got, err)
	}
	if err := os.WriteFile(path, encoded[:len(encoded)-1], 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareAgentInputImages(context.Background(), t.TempDir(), []string{path}); err == nil {
		t.Fatal("truncated WebP container accepted")
	}
}

func TestAgentViewingImagePreservesEXIFOrientation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "phone-photo.jpg")
	encoded := agentTestImage(t, path, image.Rect(0, 0, 2400, 1200), "jpeg", false)
	// Little-endian TIFF with a single SHORT Orientation=6 (90 degrees CW).
	exif := []byte{'E', 'x', 'i', 'f', 0, 0, 'I', 'I', 42, 0, 8, 0, 0, 0, 1, 0, 0x12, 1, 3, 0, 1, 0, 0, 0, 6, 0, 0, 0, 0, 0, 0, 0}
	segment := append([]byte{0xff, 0xe1, byte((len(exif) + 2) >> 8), byte(len(exif) + 2)}, exif...)
	withOrientation := append(bytes.Clone(encoded[:2]), segment...)
	withOrientation = append(withOrientation, encoded[2:]...)
	if err := os.WriteFile(path, withOrientation, 0o600); err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareAgentInputImages(context.Background(), t.TempDir(), []string{path})
	if err != nil {
		t.Fatal(err)
	}
	view, _ := agentTestReadImage(t, prepared[0])
	if view.Bounds().Dx() != 1024 || view.Bounds().Dy() != 2048 {
		t.Fatalf("EXIF orientation lost: %v", view.Bounds())
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, withOrientation) {
		t.Fatalf("original EXIF changed: %v", err)
	}
}

func TestAgentViewingOrientationsMapPixelsWithoutDistortion(t *testing.T) {
	source := image.NewRGBA(image.Rect(2, 4, 5, 6))
	for y := 4; y < 6; y++ {
		for x := 2; x < 5; x++ {
			source.SetRGBA(x, y, color.RGBA{R: uint8((y-4)*3 + x - 1), A: 255})
		}
	}
	for orientation, want := range map[int][]uint8{
		1: {1, 2, 3, 4, 5, 6}, 2: {3, 2, 1, 6, 5, 4}, 3: {6, 5, 4, 3, 2, 1}, 4: {4, 5, 6, 1, 2, 3},
		5: {1, 4, 2, 5, 3, 6}, 6: {4, 1, 5, 2, 6, 3}, 7: {6, 3, 5, 2, 4, 1}, 8: {3, 6, 2, 5, 1, 4},
	} {
		view := orientAgentImage(source, orientation)
		index := 0
		for y := view.Bounds().Min.Y; y < view.Bounds().Max.Y; y++ {
			for x := view.Bounds().Min.X; x < view.Bounds().Max.X; x++ {
				r, _, _, _ := view.At(x, y).RGBA()
				if uint8(r>>8) != want[index] {
					t.Fatalf("orientation %d pixel %d=%d, want %d", orientation, index, r>>8, want[index])
				}
				index++
			}
		}
	}
}

func agentTestImage(t *testing.T, path string, bounds image.Rectangle, format string, noise bool) []byte {
	t.Helper()
	source := image.NewNRGBA(bounds)
	state := uint32(1234567)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			shade := color.NRGBA{R: 210, G: 70, B: 35, A: 255}
			if noise {
				state ^= state << 13
				state ^= state >> 17
				state ^= state << 5
				shade.R, shade.G, shade.B = uint8(state), uint8(state>>8), uint8(state>>16)
			} else if x >= bounds.Min.X+bounds.Dx()/2 {
				shade.R, shade.G, shade.B = 20, 90, 220
			}
			source.SetNRGBA(x, y, shade)
		}
	}
	return agentTestEncodeFile(t, path, source, format)
}

func agentTestEncodeFile(t *testing.T, path string, source image.Image, format string) []byte {
	t.Helper()
	var encoded bytes.Buffer
	var err error
	if format == "jpeg" {
		err = jpeg.Encode(&encoded, source, &jpeg.Options{Quality: 90})
	} else {
		err = png.Encode(&encoded, source)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func agentTestReadImage(t *testing.T, path string) (image.Image, []byte) {
	t.Helper()
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	decoded, _, err := image.Decode(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	return decoded, encoded
}

func agentTestPNGHeader(width, height int) []byte {
	var output bytes.Buffer
	output.WriteString("\x89PNG\r\n\x1a\n")
	data := make([]byte, 13)
	binary.BigEndian.PutUint32(data, uint32(width))
	binary.BigEndian.PutUint32(data[4:], uint32(height))
	data[8], data[9] = 8, 2
	_ = binary.Write(&output, binary.BigEndian, uint32(len(data)))
	chunk := append([]byte("IHDR"), data...)
	output.Write(chunk)
	_ = binary.Write(&output, binary.BigEndian, crc32.ChecksumIEEE(chunk))
	return output.Bytes()
}
