package codexruntime

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domain "github.com/shotforward/shotforward/daemon/internal/domain/project"
	"github.com/shotforward/shotforward/daemon/internal/generation"
)

func TestCodexImageReferenceLimitAcceptsFiveAndRejectsSix(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	request := validImageGeneratorRequest(root, codexImageVersion, 0)
	for index := 0; index < 6; index++ {
		suffix := string(rune('A' + index))
		reference := domain.GenerationArtifactReference{Role: domain.GenerationReferenceImage, ArtifactID: domain.ArtifactID("art_" + strings.Repeat(suffix, 26))}
		request.Snapshot.References = append(request.Snapshot.References, reference)
		request.References = append(request.References, generation.GeneratorArtifactReference{Input: reference,
			Artifact: domain.Artifact{ID: reference.ArtifactID, ProjectID: domain.ProjectID(testProjectID), Kind: domain.MediaKindImage,
				RelativePath: "media/images/reference-" + suffix + ".png", SHA256: fmt.Sprintf("%064x", index), ByteSize: 100,
				MIMEType: "image/png", OriginalFilename: "reference-" + suffix + ".png", Width: 64, Height: 64,
				CreatedAt: time.Date(2026, 9, 4, 1, 2, 3, 0, time.UTC)},
			AbsolutePath: filepath.Join(root, "reference-"+suffix+".png")})
		if index == 4 {
			if err := validateCodexImageRequest(request); err != nil {
				t.Fatalf("five Codex references were rejected: %v", err)
			}
		}
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("six-reference fixture is otherwise invalid: %v", err)
	}
	if err := validateCodexImageRequest(request); err == nil {
		t.Fatal("six Codex references were accepted")
	}
}
