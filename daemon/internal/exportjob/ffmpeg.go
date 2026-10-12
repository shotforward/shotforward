package exportjob

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type commandRunner interface {
	Run(context.Context, string, []string) error
}

type FFmpegProcessor struct {
	executable string
	runner     commandRunner
}

func NewFFmpegProcessor(configuredExecutable string) (*FFmpegProcessor, error) {
	path, err := exec.LookPath(configuredExecutable)
	if err != nil {
		return nil, errors.New("create FFmpeg export processor: executable is unavailable")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, errors.New("create FFmpeg export processor: executable path is invalid")
	}
	return &FFmpegProcessor{executable: filepath.Clean(absolute), runner: osCommandRunner{executable: filepath.Clean(absolute)}}, nil
}

func (p *FFmpegProcessor) Export(ctx context.Context, request Request, progress Progress) (string, error) {
	if p == nil || p.runner == nil || ctx == nil || request.Validate() != nil || progress == nil {
		return "", errors.New("export episode: invalid request")
	}
	exports := filepath.Join(request.ProjectRoot, "exports")
	staging := filepath.Join(request.ProjectRoot, ".shotforward", "staging")
	if err := ensureExportDirectory(exports); err != nil || ensureExportDirectory(staging) != nil {
		return "", errors.New("export episode: project export directories are unavailable")
	}
	relative := "exports/" + request.JobID.String() + ".mp4"
	output := filepath.Join(request.ProjectRoot, filepath.FromSlash(relative))
	if readyExport(output) {
		return relative, nil
	}
	work, err := os.MkdirTemp(staging, "export-"+request.JobID.String()+"-")
	if err != nil {
		return "", errors.New("export episode: cannot create staging directory")
	}
	defer os.RemoveAll(work)
	segments := make([]string, 0, len(request.Clips))
	for index, clip := range request.Clips {
		if err := validateClipFile(request.ProjectRoot, clip); err != nil {
			return "", err
		}
		if err := progress("transcoding", fmt.Sprintf("Preparing clip %d of %d", index+1, len(request.Clips))); err != nil {
			return "", err
		}
		segmentName := fmt.Sprintf("segment-%06d.mp4", index+1)
		segmentPath := filepath.Join(work, segmentName)
		args := segmentArguments(clip, request.OutputWidth, request.OutputHeight, segmentPath)
		if err := p.runner.Run(ctx, work, args); err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", errors.New("export episode: FFmpeg could not normalize a clip")
		}
		if !readyExport(segmentPath) {
			return "", errors.New("export episode: FFmpeg produced an invalid clip")
		}
		segments = append(segments, segmentName)
	}
	if err := progress("concatenating", "Joining exported clips"); err != nil {
		return "", err
	}
	listPath := filepath.Join(work, "segments.ffconcat")
	var list strings.Builder
	list.WriteString("ffconcat version 1.0\n")
	for _, segment := range segments {
		list.WriteString("file '")
		list.WriteString(segment)
		list.WriteString("'\n")
	}
	if err := os.WriteFile(listPath, []byte(list.String()), 0o600); err != nil {
		return "", errors.New("export episode: cannot write concat manifest")
	}
	temporary := filepath.Join(work, "episode.mp4")
	if err := p.runner.Run(ctx, work, []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y", "-f", "concat", "-safe", "1", "-i", filepath.Base(listPath), "-c", "copy", "-movflags", "+faststart", temporary}); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errors.New("export episode: FFmpeg could not join clips")
	}
	if !readyExport(temporary) {
		return "", errors.New("export episode: FFmpeg produced an invalid output")
	}
	if err := os.Rename(temporary, output); err != nil {
		return "", errors.New("export episode: cannot publish output")
	}
	return relative, nil
}

func (p *FFmpegProcessor) RemoveOutput(projectRoot, relative string) error {
	if projectRoot == "" || !filepath.IsAbs(projectRoot) || relative == "" || filepath.ToSlash(filepath.Clean(filepath.FromSlash(relative))) != relative || !strings.HasPrefix(relative, "exports/exj_") || !strings.HasSuffix(relative, ".mp4") {
		return errors.New("remove export output: invalid path")
	}
	path := filepath.Join(projectRoot, filepath.FromSlash(relative))
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("remove export output: invalid file")
	}
	return os.Remove(path)
}

func segmentArguments(clip ClipSource, width, height int, output string) []string {
	start := seconds(clip.Snapshot.TrimInMilliseconds)
	end := seconds(clip.Snapshot.TrimOutMilliseconds)
	duration := seconds(clip.Snapshot.TrimOutMilliseconds - clip.Snapshot.TrimInMilliseconds)
	video := fmt.Sprintf("[0:v:0]trim=start=%s:end=%s,setpts=PTS-STARTPTS,scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=black,setsar=1,fps=30,format=yuv420p[v]", start, end, width, height, width, height)
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y", "-i", clip.AbsolutePath}
	if clip.Artifact.AudioSampleRate > 0 && clip.Artifact.AudioChannels > 0 {
		video += fmt.Sprintf(";[0:a:0]atrim=start=%s:end=%s,asetpts=PTS-STARTPTS,aresample=48000[a]", start, end)
	} else {
		args = append(args, "-f", "lavfi", "-t", duration, "-i", "anullsrc=r=48000:cl=stereo")
		video += fmt.Sprintf(";[1:a:0]atrim=duration=%s,asetpts=PTS-STARTPTS[a]", duration)
	}
	return append(args, "-filter_complex", video, "-map", "[v]", "-map", "[a]", "-c:v", "libx264", "-preset", "medium", "-crf", "18", "-c:a", "aac", "-b:a", "192k", "-ar", "48000", "-ac", "2", "-movflags", "+faststart", output)
}

func seconds(milliseconds int64) string {
	return strconv.FormatFloat(float64(milliseconds)/1000, 'f', 3, 64)
}

func validateClipFile(projectRoot string, clip ClipSource) error {
	mediaRoot := filepath.Join(projectRoot, "media")
	canonicalMedia, err := filepath.EvalSymlinks(mediaRoot)
	if err != nil {
		return errors.New("export episode: project media is unavailable")
	}
	canonicalClip, err := filepath.EvalSymlinks(clip.AbsolutePath)
	if err != nil {
		return errors.New("export episode: clip file is unavailable")
	}
	relative, err := filepath.Rel(canonicalMedia, canonicalClip)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return errors.New("export episode: clip is outside project media")
	}
	info, err := os.Lstat(clip.AbsolutePath)
	if err != nil || !info.Mode().IsRegular() || info.Size() != clip.Artifact.ByteSize || clip.Snapshot.TrimOutMilliseconds > clip.Artifact.DurationMilliseconds {
		return errors.New("export episode: clip file is unavailable")
	}
	file, err := os.Open(clip.AbsolutePath)
	if err != nil {
		return errors.New("export episode: clip file is unavailable")
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || hex.EncodeToString(hash.Sum(nil)) != clip.Artifact.SHA256 {
		return errors.New("export episode: clip file identity changed")
	}
	return nil
}

func ensureExportDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("invalid export directory")
	}
	return nil
}

func readyExport(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() > 0
}

type osCommandRunner struct{ executable string }

func (r osCommandRunner) Run(ctx context.Context, directory string, arguments []string) error {
	command := exec.CommandContext(ctx, r.executable, arguments...)
	command.Dir = directory
	command.Stdin = nil
	command.Stdout = io.Discard
	stderr := &boundedBuffer{maximum: 64 << 10}
	command.Stderr = stderr
	return command.Run()
}

type boundedBuffer struct {
	buffer  bytes.Buffer
	maximum int
}

func (b *boundedBuffer) Write(value []byte) (int, error) {
	original := len(value)
	remaining := b.maximum - b.buffer.Len()
	if remaining > 0 {
		if len(value) > remaining {
			value = value[:remaining]
		}
		_, _ = b.buffer.Write(value)
	}
	return original, nil
}
