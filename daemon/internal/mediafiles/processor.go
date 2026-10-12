package mediafiles

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
)

const stagingDirectory = "media/.staging"

const (
	maximumImageBytes int64 = 256 << 20
	maximumAudioBytes int64 = 4 << 30
	maximumVideoBytes int64 = 100 << 30
)

type Processor struct {
	ffprobe string
	ffmpeg  string
}

func NewProcessor(ffprobePath, ffmpegPath string) (Processor, error) {
	if strings.TrimSpace(ffprobePath) == "" || strings.TrimSpace(ffmpegPath) == "" {
		return Processor{}, errors.New("ffprobe and ffmpeg paths are required")
	}
	return Processor{ffprobe: ffprobePath, ffmpeg: ffmpegPath}, nil
}

func (p Processor) Prepare(ctx context.Context, request projectapp.PrepareMediaRequest) (_ projectapp.PreparedMedia, returnErr error) {
	if ctx == nil {
		return projectapp.PreparedMedia{}, fmt.Errorf("%w: context is required", projectapp.ErrMediaImportInvalid)
	}
	if !request.Kind.IsValid() || request.ProjectRoot == "" || request.SourcePath == "" {
		return projectapp.PreparedMedia{}, fmt.Errorf("%w: invalid request", projectapp.ErrMediaImportInvalid)
	}
	if (request.RequiredWidth == 0) != (request.RequiredHeight == 0) || request.RequiredWidth < 0 || request.RequiredHeight < 0 || request.RequiredWidth > 16384 || request.RequiredHeight > 16384 {
		return projectapp.PreparedMedia{}, fmt.Errorf("%w: invalid required dimensions", projectapp.ErrMediaImportInvalid)
	}
	if request.CropToFill && (request.Kind != domain.MediaKindImage || request.RequiredWidth == 0) {
		return projectapp.PreparedMedia{}, fmt.Errorf("%w: crop-to-fill requires image dimensions", projectapp.ErrMediaImportInvalid)
	}
	if request.TrimStartMilliseconds < 0 || request.TrimEndMilliseconds < 0 || request.TrimEndMilliseconds > 0 && request.TrimEndMilliseconds <= request.TrimStartMilliseconds || request.Kind == domain.MediaKindImage && (request.TrimStartMilliseconds != 0 || request.TrimEndMilliseconds != 0) {
		return projectapp.PreparedMedia{}, fmt.Errorf("%w: invalid trim range", projectapp.ErrMediaImportInvalid)
	}
	info, err := os.Lstat(request.SourcePath)
	if err != nil {
		return projectapp.PreparedMedia{}, fmt.Errorf("%w: %v", projectapp.ErrMediaImportUnreadable, err)
	}
	if !info.Mode().IsRegular() {
		return projectapp.PreparedMedia{}, fmt.Errorf("%w: source must be a regular file", projectapp.ErrMediaImportUnreadable)
	}
	maximum := maximumBytes(request.Kind)
	if info.Size() < 1 || info.Size() > maximum {
		return projectapp.PreparedMedia{}, fmt.Errorf("%w: file size is outside the supported range", projectapp.ErrMediaImportInvalid)
	}
	ext := strings.ToLower(filepath.Ext(request.SourcePath))
	if !supportedExtension(request.Kind, ext) {
		return projectapp.PreparedMedia{}, projectapp.ErrMediaImportUnsupported
	}
	stageRoot := filepath.Join(request.ProjectRoot, filepath.FromSlash(stagingDirectory))
	if err := os.MkdirAll(stageRoot, 0o755); err != nil {
		return projectapp.PreparedMedia{}, fmt.Errorf("%w: create staging directory: %v", projectapp.ErrMediaImportUnreadable, err)
	}
	staged, err := os.CreateTemp(stageRoot, "import-*"+ext)
	if err != nil {
		return projectapp.PreparedMedia{}, fmt.Errorf("%w: create staging file: %v", projectapp.ErrMediaImportUnreadable, err)
	}
	stagedPath := staged.Name()
	defer func() {
		if returnErr != nil {
			_ = os.Remove(stagedPath)
		}
	}()
	if err := copySource(ctx, staged, request.SourcePath, maximum); err != nil {
		staged.Close()
		if ctx.Err() != nil {
			return projectapp.PreparedMedia{}, ctx.Err()
		}
		return projectapp.PreparedMedia{}, fmt.Errorf("%w: %v", projectapp.ErrMediaImportUnreadable, err)
	}
	if err := staged.Sync(); err != nil {
		staged.Close()
		return projectapp.PreparedMedia{}, fmt.Errorf("%w: sync staging file: %v", projectapp.ErrMediaImportUnreadable, err)
	}
	if err := staged.Close(); err != nil {
		return projectapp.PreparedMedia{}, fmt.Errorf("%w: close staging file: %v", projectapp.ErrMediaImportUnreadable, err)
	}
	metadata, err := p.probe(ctx, stagedPath, request.Kind, ext)
	if err != nil {
		return projectapp.PreparedMedia{}, err
	}
	if request.Kind != domain.MediaKindImage && (request.TrimStartMilliseconds != 0 || request.TrimEndMilliseconds != 0) {
		trimEnd := request.TrimEndMilliseconds
		if trimEnd == 0 {
			trimEnd = metadata.DurationMilliseconds
		}
		if trimEnd > metadata.DurationMilliseconds || trimEnd <= request.TrimStartMilliseconds {
			return projectapp.PreparedMedia{}, fmt.Errorf("%w: trim range is outside the source duration", projectapp.ErrMediaImportInvalid)
		}
		if trimEnd > 0 {
			outputExt := ".mp4"
			if request.Kind == domain.MediaKindAudio {
				outputExt = ".wav"
			}
			trimmed, err := os.CreateTemp(stageRoot, "trim-*"+outputExt)
			if err != nil {
				return projectapp.PreparedMedia{}, fmt.Errorf("%w: create trim output: %v", projectapp.ErrMediaImportUnreadable, err)
			}
			trimmedPath := trimmed.Name()
			if err := trimmed.Close(); err != nil {
				_ = os.Remove(trimmedPath)
				return projectapp.PreparedMedia{}, fmt.Errorf("%w: close trim output: %v", projectapp.ErrMediaImportUnreadable, err)
			}
			if err := os.Remove(trimmedPath); err != nil {
				return projectapp.PreparedMedia{}, fmt.Errorf("%w: prepare trim output: %v", projectapp.ErrMediaImportUnreadable, err)
			}
			start := strconv.FormatFloat(float64(request.TrimStartMilliseconds)/1000, 'f', 3, 64)
			duration := strconv.FormatFloat(float64(trimEnd-request.TrimStartMilliseconds)/1000, 'f', 3, 64)
			arguments := []string{"-nostdin", "-v", "error", "-i", stagedPath, "-ss", start, "-t", duration}
			if request.Kind == domain.MediaKindVideo {
				arguments = append(arguments, "-map", "0:v:0", "-map", "0:a:0?", "-c:v", "libx264", "-preset", "medium", "-crf", "18", "-pix_fmt", "yuv420p", "-c:a", "aac", "-movflags", "+faststart")
			} else {
				arguments = append(arguments, "-map", "0:a:0", "-vn", "-c:a", "pcm_s16le")
			}
			arguments = append(arguments, trimmedPath)
			command := exec.CommandContext(ctx, p.ffmpeg, arguments...)
			if output, err := command.CombinedOutput(); err != nil {
				_ = os.Remove(trimmedPath)
				if ctx.Err() != nil {
					return projectapp.PreparedMedia{}, ctx.Err()
				}
				return projectapp.PreparedMedia{}, fmt.Errorf("%w: trim media: %v: %s", projectapp.ErrMediaImportInvalid, err, boundedMessage(output))
			}
			if err := os.Remove(stagedPath); err != nil {
				_ = os.Remove(trimmedPath)
				return projectapp.PreparedMedia{}, fmt.Errorf("%w: replace staged media: %v", projectapp.ErrMediaImportUnreadable, err)
			}
			stagedPath, ext = trimmedPath, outputExt
			metadata, err = p.probe(ctx, stagedPath, request.Kind, ext)
			if err != nil {
				return projectapp.PreparedMedia{}, err
			}
		}
	}
	if request.RequiredWidth > 0 {
		matchingAspect := int64(metadata.Width)*int64(request.RequiredHeight) == int64(metadata.Height)*int64(request.RequiredWidth)
		if request.Kind != domain.MediaKindImage || (!matchingAspect && !request.CropToFill) {
			return projectapp.PreparedMedia{}, projectapp.ErrMediaAspectRatioMismatch
		}
		if metadata.Width != request.RequiredWidth || metadata.Height != request.RequiredHeight || ext == ".webp" || !matchingAspect {
			outputExt := ext
			if outputExt == ".webp" {
				outputExt = ".png"
			}
			resized, err := os.CreateTemp(stageRoot, "resize-*"+outputExt)
			if err != nil {
				return projectapp.PreparedMedia{}, fmt.Errorf("%w: create resize output: %v", projectapp.ErrMediaImportUnreadable, err)
			}
			resizedPath := resized.Name()
			if err := resized.Close(); err != nil {
				_ = os.Remove(resizedPath)
				return projectapp.PreparedMedia{}, fmt.Errorf("%w: close resize output: %v", projectapp.ErrMediaImportUnreadable, err)
			}
			if err := os.Remove(resizedPath); err != nil {
				return projectapp.PreparedMedia{}, fmt.Errorf("%w: prepare resize output: %v", projectapp.ErrMediaImportUnreadable, err)
			}
			filter := fmt.Sprintf("scale=%d:%d:flags=lanczos,setsar=1", request.RequiredWidth, request.RequiredHeight)
			if !matchingAspect {
				filter = fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=increase:flags=lanczos,crop=%d:%d:(iw-%d)/2:(ih-%d)/2,setsar=1", request.RequiredWidth, request.RequiredHeight, request.RequiredWidth, request.RequiredHeight, request.RequiredWidth, request.RequiredHeight)
			}
			command := exec.CommandContext(ctx, p.ffmpeg, "-nostdin", "-v", "error", "-i", stagedPath, "-vf", filter, "-frames:v", "1", resizedPath)
			if output, err := command.CombinedOutput(); err != nil {
				_ = os.Remove(resizedPath)
				if ctx.Err() != nil {
					return projectapp.PreparedMedia{}, ctx.Err()
				}
				return projectapp.PreparedMedia{}, fmt.Errorf("%w: resize image: %v: %s", projectapp.ErrMediaImportInvalid, err, boundedMessage(output))
			}
			if err := os.Remove(stagedPath); err != nil {
				_ = os.Remove(resizedPath)
				return projectapp.PreparedMedia{}, fmt.Errorf("%w: replace staging image: %v", projectapp.ErrMediaImportUnreadable, err)
			}
			stagedPath = resizedPath
			ext = outputExt
			metadata, err = p.probe(ctx, stagedPath, request.Kind, ext)
			if err != nil {
				return projectapp.PreparedMedia{}, err
			}
		}
		if metadata.Width != request.RequiredWidth || metadata.Height != request.RequiredHeight {
			return projectapp.PreparedMedia{}, fmt.Errorf("%w: resize output dimensions are invalid", projectapp.ErrMediaImportInvalid)
		}
	}
	hash, size, err := hashFile(stagedPath)
	if err != nil {
		return projectapp.PreparedMedia{}, fmt.Errorf("%w: hash staged file: %v", projectapp.ErrMediaImportUnreadable, err)
	}
	metadata.Token = filepath.Base(stagedPath)
	metadata.AbsolutePath = stagedPath
	metadata.Extension = ext
	metadata.SHA256 = hash
	metadata.ByteSize = size
	metadata.MIMEType = mimeType(request.Kind, ext)
	metadata.OriginalFilename = filepath.Base(request.SourcePath)
	if request.OriginalFilename != "" {
		clean := filepath.Base(strings.ReplaceAll(request.OriginalFilename, "\\", "/"))
		if clean != request.OriginalFilename || clean == "." || clean == "" || strings.ContainsAny(clean, "\r\n\x00") {
			return projectapp.PreparedMedia{}, fmt.Errorf("%w: invalid original filename", projectapp.ErrMediaImportInvalid)
		}
		metadata.OriginalFilename = clean
	}
	return metadata, nil
}

func (p Processor) PrepareVideoFrame(ctx context.Context, request projectapp.PrepareVideoFrameRequest) (projectapp.PreparedMedia, error) {
	if ctx == nil {
		return projectapp.PreparedMedia{}, fmt.Errorf("%w: context is required", projectapp.ErrMediaImportInvalid)
	}
	if request.ProjectRoot == "" || request.SourceRelativePath == "" || request.AtMilliseconds < 0 || request.RequiredWidth < 1 || request.RequiredHeight < 1 || request.RequiredWidth > 16384 || request.RequiredHeight > 16384 {
		return projectapp.PreparedMedia{}, fmt.Errorf("%w: invalid video frame request", projectapp.ErrMediaImportInvalid)
	}
	clean := filepath.Clean(filepath.FromSlash(request.SourceRelativePath))
	if filepath.IsAbs(clean) || clean == "." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || filepath.ToSlash(clean) != request.SourceRelativePath || !strings.HasPrefix(request.SourceRelativePath, "media/videos/") {
		return projectapp.PreparedMedia{}, fmt.Errorf("%w: invalid source video path", projectapp.ErrMediaImportInvalid)
	}
	source := filepath.Join(request.ProjectRoot, clean)
	info, err := os.Lstat(source)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maximumVideoBytes {
		return projectapp.PreparedMedia{}, fmt.Errorf("%w: source video is unavailable", projectapp.ErrMediaImportUnreadable)
	}
	stageRoot := filepath.Join(request.ProjectRoot, filepath.FromSlash(stagingDirectory))
	if err := os.MkdirAll(stageRoot, 0o755); err != nil {
		return projectapp.PreparedMedia{}, fmt.Errorf("%w: create staging directory: %v", projectapp.ErrMediaImportUnreadable, err)
	}
	extracted, err := os.CreateTemp(stageRoot, "frame-*.png")
	if err != nil {
		return projectapp.PreparedMedia{}, fmt.Errorf("%w: create frame output: %v", projectapp.ErrMediaImportUnreadable, err)
	}
	extractedPath := extracted.Name()
	if err := extracted.Close(); err != nil {
		_ = os.Remove(extractedPath)
		return projectapp.PreparedMedia{}, fmt.Errorf("%w: close frame output: %v", projectapp.ErrMediaImportUnreadable, err)
	}
	if err := os.Remove(extractedPath); err != nil {
		return projectapp.PreparedMedia{}, fmt.Errorf("%w: prepare frame output: %v", projectapp.ErrMediaImportUnreadable, err)
	}
	defer os.Remove(extractedPath)
	position := strconv.FormatFloat(float64(request.AtMilliseconds)/1000, 'f', 3, 64)
	command := exec.CommandContext(ctx, p.ffmpeg, "-nostdin", "-v", "error", "-i", source, "-ss", position, "-map", "0:v:0", "-frames:v", "1", extractedPath)
	if output, err := command.CombinedOutput(); err != nil {
		if ctx.Err() != nil {
			return projectapp.PreparedMedia{}, ctx.Err()
		}
		return projectapp.PreparedMedia{}, fmt.Errorf("%w: extract video frame: %v: %s", projectapp.ErrMediaImportInvalid, err, boundedMessage(output))
	}
	return p.Prepare(ctx, projectapp.PrepareMediaRequest{
		ProjectRoot:      request.ProjectRoot,
		SourcePath:       extractedPath,
		OriginalFilename: request.OriginalFilename,
		Kind:             domain.MediaKindImage,
		RequiredWidth:    request.RequiredWidth,
		RequiredHeight:   request.RequiredHeight,
		CropToFill:       true,
	})
}

func (p Processor) Install(ctx context.Context, projectRoot string, prepared projectapp.PreparedMedia, artifactID domain.ArtifactID) (projectapp.InstalledMedia, error) {
	if ctx == nil {
		return projectapp.InstalledMedia{}, errors.New("install media: context is required")
	}
	if err := ctx.Err(); err != nil {
		return projectapp.InstalledMedia{}, err
	}
	if err := artifactID.Validate(); err != nil {
		return projectapp.InstalledMedia{}, err
	}
	if !supportedExtension(prepared.Kind, prepared.Extension) {
		return projectapp.InstalledMedia{}, projectapp.ErrMediaImportInvalid
	}
	stagedPath, err := validatedStagingPath(projectRoot, prepared.Token)
	if err != nil {
		return projectapp.InstalledMedia{}, err
	}
	directory := ""
	switch prepared.Kind {
	case domain.MediaKindImage:
		directory = "images"
	case domain.MediaKindVideo:
		directory = "videos"
	case domain.MediaKindAudio:
		directory = "audio"
	default:
		return projectapp.InstalledMedia{}, projectapp.ErrMediaImportInvalid
	}
	relative := filepath.ToSlash(filepath.Join("media", directory, artifactID.String()+prepared.Extension))
	target := filepath.Join(projectRoot, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return projectapp.InstalledMedia{}, fmt.Errorf("install media: %w", err)
	}
	if _, err := os.Lstat(target); err == nil {
		return projectapp.InstalledMedia{}, fmt.Errorf("install media: destination exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return projectapp.InstalledMedia{}, fmt.Errorf("install media: inspect destination: %w", err)
	}
	if err := os.Rename(stagedPath, target); err != nil {
		return projectapp.InstalledMedia{}, fmt.Errorf("install media: atomic move: %w", err)
	}
	return projectapp.InstalledMedia{RelativePath: relative}, nil
}

func (p Processor) Discard(projectRoot string, prepared projectapp.PreparedMedia) error {
	path, err := validatedStagingPath(projectRoot, prepared.Token)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (p Processor) Remove(projectRoot string, installed projectapp.InstalledMedia) error {
	clean := filepath.Clean(filepath.FromSlash(installed.RelativePath))
	if installed.RelativePath == "" || filepath.IsAbs(clean) || clean == "." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || filepath.ToSlash(clean) != installed.RelativePath || !strings.HasPrefix(installed.RelativePath, "media/") {
		return errors.New("invalid installed media path")
	}
	if err := os.Remove(filepath.Join(projectRoot, clean)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

type probeOutput struct {
	Streams []struct {
		CodecType   string `json:"codec_type"`
		CodecName   string `json:"codec_name"`
		Width       int    `json:"width"`
		Height      int    `json:"height"`
		Duration    string `json:"duration"`
		AverageRate string `json:"avg_frame_rate"`
		RealRate    string `json:"r_frame_rate"`
		SampleRate  string `json:"sample_rate"`
		Channels    int    `json:"channels"`
	} `json:"streams"`
	Format struct {
		Duration   string `json:"duration"`
		FormatName string `json:"format_name"`
	} `json:"format"`
}

func (p Processor) probe(ctx context.Context, path string, kind domain.MediaKind, extension string) (projectapp.PreparedMedia, error) {
	command := exec.CommandContext(ctx, p.ffprobe, "-v", "error", "-show_streams", "-show_format", "-of", "json", path)
	output, err := command.Output()
	if err != nil {
		if ctx.Err() != nil {
			return projectapp.PreparedMedia{}, ctx.Err()
		}
		return projectapp.PreparedMedia{}, fmt.Errorf("%w: probe media: %v", projectapp.ErrMediaImportInvalid, err)
	}
	var result probeOutput
	if err := json.Unmarshal(output, &result); err != nil {
		return projectapp.PreparedMedia{}, fmt.Errorf("%w: decode probe output", projectapp.ErrMediaImportInvalid)
	}
	metadata := projectapp.PreparedMedia{Kind: kind}
	var duration string
	var primaryCodec string
	for _, stream := range result.Streams {
		switch stream.CodecType {
		case "video":
			if metadata.Width == 0 {
				primaryCodec = stream.CodecName
				metadata.Width, metadata.Height = stream.Width, stream.Height
				metadata.FPSNumerator, metadata.FPSDenominator = parseRate(firstNonEmptyRate(stream.AverageRate, stream.RealRate))
			}
			if duration == "" {
				duration = stream.Duration
			}
		case "audio":
			if metadata.AudioSampleRate == 0 {
				if kind == domain.MediaKindAudio {
					primaryCodec = stream.CodecName
				}
				metadata.AudioSampleRate, _ = strconv.Atoi(stream.SampleRate)
				metadata.AudioChannels = stream.Channels
			}
			if duration == "" {
				duration = stream.Duration
			}
		}
	}
	if duration == "" {
		duration = result.Format.Duration
	}
	metadata.DurationMilliseconds = parseDurationMilliseconds(duration)
	if !matchesFormat(kind, extension, primaryCodec, result.Format.FormatName) {
		return projectapp.PreparedMedia{}, fmt.Errorf("%w: file content does not match extension", projectapp.ErrMediaImportInvalid)
	}
	switch kind {
	case domain.MediaKindImage:
		metadata.DurationMilliseconds = 0
		metadata.FPSNumerator, metadata.FPSDenominator = 0, 0
		metadata.AudioSampleRate, metadata.AudioChannels = 0, 0
		if metadata.Width < 1 || metadata.Height < 1 {
			return projectapp.PreparedMedia{}, fmt.Errorf("%w: image stream not found", projectapp.ErrMediaImportInvalid)
		}
	case domain.MediaKindVideo:
		if metadata.Width < 1 || metadata.Height < 1 || metadata.DurationMilliseconds < 1 || metadata.FPSNumerator < 1 || metadata.FPSDenominator < 1 {
			return projectapp.PreparedMedia{}, fmt.Errorf("%w: invalid video metadata", projectapp.ErrMediaImportInvalid)
		}
	case domain.MediaKindAudio:
		metadata.Width, metadata.Height = 0, 0
		metadata.FPSNumerator, metadata.FPSDenominator = 0, 0
		if metadata.DurationMilliseconds < 1 || metadata.AudioSampleRate < 1 || metadata.AudioChannels < 1 {
			return projectapp.PreparedMedia{}, fmt.Errorf("%w: invalid audio metadata", projectapp.ErrMediaImportInvalid)
		}
	}
	return metadata, nil
}

func matchesFormat(kind domain.MediaKind, extension, codec, formatName string) bool {
	switch kind {
	case domain.MediaKindImage:
		switch extension {
		case ".png":
			return codec == "png"
		case ".jpg", ".jpeg":
			return codec == "mjpeg"
		case ".webp":
			return codec == "webp"
		}
	case domain.MediaKindVideo:
		if codec == "" {
			return false
		}
		if extension == ".webm" {
			return strings.Contains(formatName, "webm")
		}
		return (extension == ".mp4" || extension == ".mov") && strings.Contains(formatName, "mov")
	case domain.MediaKindAudio:
		switch extension {
		case ".wav":
			return formatName == "wav"
		case ".mp3":
			return formatName == "mp3"
		case ".m4a":
			return strings.Contains(formatName, "mov")
		}
	}
	return false
}

func supportedExtension(kind domain.MediaKind, extension string) bool {
	switch kind {
	case domain.MediaKindImage:
		return extension == ".png" || extension == ".jpg" || extension == ".jpeg" || extension == ".webp"
	case domain.MediaKindVideo:
		return extension == ".mp4" || extension == ".mov" || extension == ".webm"
	case domain.MediaKindAudio:
		return extension == ".wav" || extension == ".mp3" || extension == ".m4a"
	}
	return false
}

func mimeType(kind domain.MediaKind, extension string) string {
	switch extension {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".mp4":
		return "video/mp4"
	case ".mov":
		return "video/quicktime"
	case ".webm":
		return "video/webm"
	case ".wav":
		return "audio/wav"
	case ".mp3":
		return "audio/mpeg"
	case ".m4a":
		return "audio/mp4"
	}
	return string(kind) + "/unknown"
}

func copySource(ctx context.Context, destination *os.File, sourcePath string, maximum int64) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	buffer := make([]byte, 1024*1024)
	written, err := io.CopyBuffer(destination, &contextReader{ctx: ctx, reader: io.LimitReader(source, maximum+1)}, buffer)
	if err != nil {
		return err
	}
	if written > maximum {
		return errors.New("media file exceeds size limit")
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

func maximumBytes(kind domain.MediaKind) int64 {
	switch kind {
	case domain.MediaKindImage:
		return maximumImageBytes
	case domain.MediaKindAudio:
		return maximumAudioBytes
	case domain.MediaKindVideo:
		return maximumVideoBytes
	default:
		return 0
	}
}

func hashFile(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

func validatedStagingPath(root, token string) (string, error) {
	if token == "" || token != filepath.Base(token) || strings.ContainsAny(token, `/\\`) {
		return "", errors.New("invalid media staging token")
	}
	return filepath.Join(root, filepath.FromSlash(stagingDirectory), token), nil
}

func parseDurationMilliseconds(value string) int64 {
	seconds, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 {
		return 0
	}
	return int64(math.Round(seconds * 1000))
}

func parseRate(value string) (int64, int64) {
	parts := strings.Split(value, "/")
	if len(parts) != 2 {
		return 0, 0
	}
	numerator, errN := strconv.ParseInt(parts[0], 10, 64)
	denominator, errD := strconv.ParseInt(parts[1], 10, 64)
	if errN != nil || errD != nil || numerator < 1 || denominator < 1 {
		return 0, 0
	}
	return numerator, denominator
}

func firstNonEmptyRate(values ...string) string {
	for _, value := range values {
		if value != "" && value != "0/0" {
			return value
		}
	}
	return ""
}

func boundedMessage(value []byte) string {
	message := strings.TrimSpace(string(value))
	if len(message) > 512 {
		return message[:512]
	}
	return message
}
