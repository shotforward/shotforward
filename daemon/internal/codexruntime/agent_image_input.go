package codexruntime

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"
)

const (
	agentImageCacheDirectory = ".agent-image-inputs-v1"
	maximumAgentImageBytes   = 1 << 20
	maximumAgentImageEdge    = 2048
	maximumAgentSourceBytes  = 256 << 20
	maximumAgentSourceEdge   = 32768
	maximumAgentSourcePixels = 40_000_000
	// Include every output-affecting choice in the content-addressed cache key.
	agentImageEncodingParameters = "agent-image-v1;jpeg;edge=2048;quality=82,75,65;min-edge=768;max-bytes=1048576;bilinear;white;exif-orientation"
)

// prepareAgentInputImages prepares only the Agent's visual conversation inputs.
// Original Artifacts (including references used for image generation) are never
// changed. JPEG/PNG viewing copies fit 1 MiB; small originals retain their
// quality and transparency. WebP retains the Runtime's existing decoder path.
func prepareAgentInputImages(ctx context.Context, workingDirectory string, paths []string) ([]string, error) {
	if ctx == nil {
		return nil, errors.New("prepare Agent image input: context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, nil
	}
	if workingDirectory == "" || !filepath.IsAbs(workingDirectory) || filepath.Clean(workingDirectory) != workingDirectory {
		return nil, errors.New("prepare Agent image input: clean absolute cache root is required")
	}
	rootInfo, err := os.Lstat(workingDirectory)
	if err != nil || !rootInfo.IsDir() {
		return nil, errors.New("prepare Agent image input: cache root must be a directory, not a symbolic link")
	}
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		prepared, err := prepareAgentInputImage(ctx, workingDirectory, path)
		if err != nil {
			return nil, err
		}
		result = append(result, prepared)
	}
	return result, nil
}

func prepareAgentInputImage(ctx context.Context, workingDirectory, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("prepare Agent image input: clean absolute source path is required")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("prepare Agent image input: source must be a regular file, not a symbolic link")
	}
	if info.Size() < 1 || info.Size() > maximumAgentSourceBytes {
		return "", errors.New("prepare Agent image input: source file is outside the supported size range")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", errors.New("prepare Agent image input: source could not be opened")
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) || info.Size() != openedInfo.Size() {
		return "", errors.New("prepare Agent image input: source changed while opening")
	}
	reader := func() io.Reader {
		return agentImageContextReader{ctx, io.NewSectionReader(file, 0, info.Size())}
	}
	var signature [12]byte
	if _, err := file.ReadAt(signature[:], 0); err == nil && string(signature[:4]) == "RIFF" && string(signature[8:]) == "WEBP" {
		// No WebP decoder is available in the standard library. Preserve this
		// previously supported format instead of imposing a new viewing limit;
		// the Runtime decodes it and the proxy enforces its complete-body budget.
		// Check the container length without reading a potentially large image.
		if uint64(binary.LittleEndian.Uint32(signature[4:8]))+8 != uint64(info.Size()) {
			return "", errors.New("prepare Agent image input: WebP container is incomplete")
		}
		if err := unchangedAgentImageSource(file, info); err != nil {
			return "", err
		}
		return path, ctx.Err()
	}
	config, format, err := image.DecodeConfig(reader())
	if err != nil {
		return "", agentImageReadError(ctx, "image metadata could not be decoded")
	}
	if (format != "jpeg" && format != "png") || !validAgentImageDimensions(config.Width, config.Height) {
		return "", errors.New("prepare Agent image input: unsupported format or dimensions")
	}
	if info.Size() <= maximumAgentImageBytes && config.Width <= maximumAgentImageEdge && config.Height <= maximumAgentImageEdge {
		// Decode even passthrough images so truncated pixel data fails locally.
		if _, _, err := image.Decode(reader()); err != nil {
			return "", agentImageReadError(ctx, "image pixels could not be decoded")
		}
		if err := unchangedAgentImageSource(file, info); err != nil {
			return "", err
		}
		return path, ctx.Err()
	}
	hash := sha256.New()
	_, _ = io.WriteString(hash, agentImageEncodingParameters+"\x00")
	if _, err := io.Copy(hash, reader()); err != nil {
		return "", err
	}
	if err := unchangedAgentImageSource(file, info); err != nil {
		return "", err
	}
	name := hex.EncodeToString(hash.Sum(nil)) + ".jpg"
	cachePath := filepath.Join(workingDirectory, agentImageCacheDirectory, name)
	release, err := acquireAgentImagePreparation(ctx, cachePath)
	if err != nil {
		return "", err
	}
	defer release()
	cache, err := openAgentImageCache(workingDirectory)
	if err != nil {
		return "", err
	}
	defer cache.Close()
	if cached, err := cachedAgentImage(ctx, cache, name); err != nil {
		return "", err
	} else if cached {
		if err := unchangedAgentImageSource(file, info); err != nil {
			return "", err
		}
		return cachePath, ctx.Err()
	}
	source, _, err := image.Decode(reader())
	if err != nil {
		return "", agentImageReadError(ctx, "image pixels could not be decoded")
	}
	if err := unchangedAgentImageSource(file, info); err != nil {
		return "", err
	}
	if format == "jpeg" {
		orientation, err := agentJPEGOrientation(reader())
		if err != nil {
			return "", agentImageReadError(ctx, "JPEG metadata could not be decoded")
		}
		source = orientAgentImage(source, orientation)
	}
	encoded, err := encodeAgentViewingImage(ctx, source)
	if err != nil {
		return "", err
	}
	if err := writeAgentImageCache(ctx, cache, name, encoded); err != nil {
		return "", err
	}
	return cachePath, ctx.Err()
}

func validAgentImageDimensions(width, height int) bool {
	return width > 0 && height > 0 && width <= maximumAgentSourceEdge && height <= maximumAgentSourceEdge && int64(width)*int64(height) <= maximumAgentSourcePixels
}

func unchangedAgentImageSource(file *os.File, previous os.FileInfo) error {
	current, err := file.Stat()
	if err != nil || current.Size() != previous.Size() || !current.ModTime().Equal(previous.ModTime()) {
		return errors.New("prepare Agent image input: source changed while preparing")
	}
	pathInfo, err := os.Lstat(file.Name())
	if err != nil || !pathInfo.Mode().IsRegular() || !os.SameFile(current, pathInfo) {
		return errors.New("prepare Agent image input: source path changed while preparing")
	}
	return nil
}

func agentImageReadError(ctx context.Context, detail string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return errors.New("prepare Agent image input: " + detail)
}

func openAgentImageCache(workingDirectory string) (*os.Root, error) {
	root, err := os.OpenRoot(workingDirectory)
	if err != nil {
		return nil, errors.New("prepare Agent image input: cache root could not be opened")
	}
	defer root.Close()
	if err := root.Mkdir(agentImageCacheDirectory, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, errors.New("prepare Agent image input: cache directory could not be created")
	}
	info, err := root.Lstat(agentImageCacheDirectory)
	if err != nil || !info.IsDir() {
		return nil, errors.New("prepare Agent image input: cache directory must not be a symbolic link")
	}
	cache, err := root.OpenRoot(agentImageCacheDirectory)
	if err != nil {
		return nil, errors.New("prepare Agent image input: cache directory could not be opened")
	}
	if err := cache.Chmod(".", 0o700); err != nil {
		cache.Close()
		return nil, errors.New("prepare Agent image input: cache permissions could not be secured")
	}
	return cache, nil
}

func cachedAgentImage(ctx context.Context, cache *os.Root, name string) (bool, error) {
	info, err := cache.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return false, errors.New("prepare Agent image input: cached image must be a regular file, not a symbolic link")
	}
	valid := false
	if info.Size() > 0 && info.Size() <= maximumAgentImageBytes {
		file, err := cache.Open(name)
		if err != nil {
			return false, errors.New("prepare Agent image input: cached image could not be opened")
		}
		config, format, decodeErr := image.DecodeConfig(agentImageContextReader{ctx, io.NewSectionReader(file, 0, info.Size())})
		valid = decodeErr == nil && format == "jpeg" && config.Width > 0 && config.Height > 0 && config.Width <= maximumAgentImageEdge && config.Height <= maximumAgentImageEdge
		if valid {
			_, _, decodeErr = image.Decode(agentImageContextReader{ctx, io.NewSectionReader(file, 0, info.Size())})
			valid = decodeErr == nil
		}
		file.Close()
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !valid {
		if err := cache.Remove(name); err != nil {
			return false, errors.New("prepare Agent image input: damaged cached image could not be replaced")
		}
	}
	return valid, nil
}

func encodeAgentViewingImage(ctx context.Context, source image.Image) ([]byte, error) {
	edge := min(maximumAgentImageEdge, max(source.Bounds().Dx(), source.Bounds().Dy()))
	for {
		view, err := resizeAgentImage(ctx, source, edge)
		if err != nil {
			return nil, err
		}
		for _, quality := range []int{82, 75, 65} {
			var output bytes.Buffer
			if err := jpeg.Encode(agentImageContextWriter{ctx, &output}, view, &jpeg.Options{Quality: quality}); err != nil {
				return nil, agentImageReadError(ctx, "viewing copy could not be encoded")
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if output.Len() <= maximumAgentImageBytes {
				return output.Bytes(), nil
			}
		}
		if edge <= 768 {
			return nil, errors.New("prepare Agent image input: viewing copy could not fit the 1 MiB limit")
		}
		edge = max(768, edge*3/4)
	}
}

// Bilinear sampling preserves aspect ratio and composites premultiplied alpha
// onto white. Source bounds may be nonzero (or exposed through EXIF rotation).
func resizeAgentImage(ctx context.Context, source image.Image, edge int) (*image.RGBA, error) {
	bounds := source.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if !validAgentImageDimensions(w, h) || edge < 1 {
		return nil, errors.New("prepare Agent image input: unsupported source dimensions")
	}
	longest := max(w, h)
	edge = min(edge, longest)
	width, height := max(1, w*edge/longest), max(1, h*edge/longest)
	result := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		if y%16 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		sy := max(0, (float64(y)+0.5)*float64(h)/float64(height)-0.5)
		y0 := int(sy)
		y1, fy := min(y0+1, h-1), sy-float64(y0)
		for x := 0; x < width; x++ {
			sx := max(0, (float64(x)+0.5)*float64(w)/float64(width)-0.5)
			x0 := int(sx)
			x1, fx := min(x0+1, w-1), sx-float64(x0)
			weights := [4]float64{(1 - fx) * (1 - fy), fx * (1 - fy), (1 - fx) * fy, fx * fy}
			positions := [4]image.Point{{x0, y0}, {x1, y0}, {x0, y1}, {x1, y1}}
			var channels [3]float64
			for index, position := range positions {
				r, g, b, a := source.At(bounds.Min.X+position.X, bounds.Min.Y+position.Y).RGBA()
				white := 65535 - a
				channels[0] += float64(r+white) * weights[index]
				channels[1] += float64(g+white) * weights[index]
				channels[2] += float64(b+white) * weights[index]
			}
			position := y*result.Stride + x*4
			for channel := range channels {
				result.Pix[position+channel] = uint8(math.Round(channels[channel] / 257))
			}
			result.Pix[position+3] = 255
		}
	}
	return result, ctx.Err()
}

func writeAgentImageCache(ctx context.Context, cache *os.Root, name string, encoded []byte) error {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return errors.New("prepare Agent image input: cache temporary name could not be generated")
	}
	temporary := ".pending-" + hex.EncodeToString(random[:])
	file, err := cache.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return errors.New("prepare Agent image input: cached image could not be created")
	}
	defer cache.Remove(temporary)
	_, writeErr := agentImageContextWriter{ctx, file}.Write(encoded)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	if writeErr != nil || closeErr != nil {
		return errors.New("prepare Agent image input: cached image could not be written")
	}
	if err := cache.Rename(temporary, name); err != nil {
		// Another process can publish the identical content-addressed image.
		if exists, cachedErr := cachedAgentImage(ctx, cache, name); cachedErr == nil && exists {
			return nil
		}
		return errors.New("prepare Agent image input: cached image could not be published")
	}
	return nil
}

type agentImageContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r agentImageContextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

type agentImageContextWriter struct {
	ctx    context.Context
	writer io.Writer
}

func (w agentImageContextWriter) Write(buffer []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return w.writer.Write(buffer)
}

type agentImagePreparationGate struct {
	token chan struct{}
	users int
}

var agentImagePreparationGates = struct {
	sync.Mutex
	entries map[string]*agentImagePreparationGate
}{entries: make(map[string]*agentImagePreparationGate)}

// Retire entries when their last waiter leaves; no global cache of user paths.
func acquireAgentImagePreparation(ctx context.Context, key string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	agentImagePreparationGates.Lock()
	gate := agentImagePreparationGates.entries[key]
	if gate == nil {
		gate = &agentImagePreparationGate{token: make(chan struct{}, 1)}
		agentImagePreparationGates.entries[key] = gate
	}
	gate.users++
	agentImagePreparationGates.Unlock()
	retire := func() {
		agentImagePreparationGates.Lock()
		defer agentImagePreparationGates.Unlock()
		gate.users--
		if gate.users == 0 {
			delete(agentImagePreparationGates.entries, key)
		}
	}
	select {
	case gate.token <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-gate.token
			retire()
			return nil, err
		}
		return func() { <-gate.token; retire() }, nil
	case <-ctx.Done():
		retire()
		return nil, ctx.Err()
	}
}

// Applying EXIF rotation before encoding prevents phone photos from rotating
// when JPEG metadata is omitted from the smaller viewing copy.
func agentJPEGOrientation(reader io.Reader) (int, error) {
	input := bufio.NewReader(reader)
	var prefix [2]byte
	if _, err := io.ReadFull(input, prefix[:]); err != nil || prefix != [2]byte{0xff, 0xd8} {
		return 1, errors.New("invalid JPEG header")
	}
	for {
		marker, err := input.ReadByte()
		if err != nil || marker != 0xff {
			return 1, errors.New("invalid JPEG marker")
		}
		for marker == 0xff {
			marker, err = input.ReadByte()
			if err != nil {
				return 1, err
			}
		}
		if marker == 0xda || marker == 0xd9 {
			return 1, nil
		}
		if marker == 0x01 || marker >= 0xd0 && marker <= 0xd7 {
			continue
		}
		if _, err := io.ReadFull(input, prefix[:]); err != nil {
			return 1, err
		}
		length := int(binary.BigEndian.Uint16(prefix[:])) - 2
		if length < 0 {
			return 1, errors.New("invalid JPEG segment length")
		}
		if marker != 0xe1 {
			if _, err := io.CopyN(io.Discard, input, int64(length)); err != nil {
				return 1, err
			}
			continue
		}
		data := make([]byte, length)
		if _, err := io.ReadFull(input, data); err != nil {
			return 1, err
		}
		if len(data) >= 6 && bytes.Equal(data[:6], []byte("Exif\x00\x00")) {
			return agentEXIFOrientation(data[6:]), nil
		}
	}
}

func agentEXIFOrientation(data []byte) int {
	if len(data) < 8 {
		return 1
	}
	var order binary.ByteOrder
	switch string(data[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 1
	}
	if order.Uint16(data[2:4]) != 42 {
		return 1
	}
	offset := uint64(order.Uint32(data[4:8]))
	if offset+2 > uint64(len(data)) {
		return 1
	}
	count := uint64(order.Uint16(data[offset : offset+2]))
	for entry := offset + 2; count > 0 && entry+12 <= uint64(len(data)); entry, count = entry+12, count-1 {
		value := data[entry : entry+12]
		if order.Uint16(value[:2]) == 0x112 && order.Uint16(value[2:4]) == 3 && order.Uint32(value[4:8]) == 1 {
			orientation := int(order.Uint16(value[8:10]))
			if orientation >= 1 && orientation <= 8 {
				return orientation
			}
		}
	}
	return 1
}

type agentOrientedImage struct {
	image.Image
	orientation int
}

func orientAgentImage(source image.Image, orientation int) image.Image {
	if orientation <= 1 || orientation > 8 {
		return source
	}
	return agentOrientedImage{source, orientation}
}

func (m agentOrientedImage) Bounds() image.Rectangle {
	bounds := m.Image.Bounds()
	if m.orientation >= 5 {
		return image.Rect(0, 0, bounds.Dy(), bounds.Dx())
	}
	return image.Rect(0, 0, bounds.Dx(), bounds.Dy())
}

func (m agentOrientedImage) At(x, y int) color.Color {
	bounds := m.Image.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	switch m.orientation {
	case 2:
		x = w - 1 - x
	case 3:
		x, y = w-1-x, h-1-y
	case 4:
		y = h - 1 - y
	case 5:
		x, y = y, x
	case 6:
		x, y = y, h-1-x
	case 7:
		x, y = w-1-y, h-1-x
	case 8:
		x, y = w-1-y, x
	}
	return m.Image.At(bounds.Min.X+x, bounds.Min.Y+y)
}
