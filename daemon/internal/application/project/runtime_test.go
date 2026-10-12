package projectapp

import (
	"bytes"
	"testing"
	"time"
)

func TestSecureIDGeneratorCreatesValidTypedIDs(t *testing.T) {
	t.Parallel()

	generator := NewSecureIDGenerator()
	projectID, err := generator.NewProjectID()
	if err != nil {
		t.Fatalf("NewProjectID() error = %v", err)
	}
	if err := projectID.Validate(); err != nil {
		t.Fatalf("generated Project ID is invalid: %v", err)
	}
	episodeID, err := generator.NewEpisodeID()
	if err != nil {
		t.Fatalf("NewEpisodeID() error = %v", err)
	}
	if err := episodeID.Validate(); err != nil {
		t.Fatalf("generated Episode ID is invalid: %v", err)
	}
	assetID, err := generator.NewAssetID()
	if err != nil {
		t.Fatalf("NewAssetID() error = %v", err)
	}
	if err := assetID.Validate(); err != nil {
		t.Fatalf("generated Asset ID is invalid: %v", err)
	}
	identityID, err := generator.NewAssetIdentityID()
	if err != nil {
		t.Fatalf("NewAssetIdentityID() error = %v", err)
	}
	if err := identityID.Validate(); err != nil {
		t.Fatalf("generated Asset Identity ID is invalid: %v", err)
	}
	sceneID, err := generator.NewSceneID()
	if err != nil || sceneID.Validate() != nil {
		t.Fatalf("NewSceneID() = %q, %v", sceneID, err)
	}
	clipID, err := generator.NewClipID()
	if err != nil || clipID.Validate() != nil {
		t.Fatalf("NewClipID() = %q, %v", clipID, err)
	}
	artifactID, err := generator.NewArtifactID()
	if err != nil || artifactID.Validate() != nil {
		t.Fatalf("NewArtifactID() = %q, %v", artifactID, err)
	}
	versionID, err := generator.NewMediaVersionID()
	if err != nil || versionID.Validate() != nil {
		t.Fatalf("NewMediaVersionID() = %q, %v", versionID, err)
	}
	audioID, err := generator.NewAudioReferenceAssetID()
	if err != nil || audioID.Validate() != nil {
		t.Fatalf("NewAudioReferenceAssetID() = %q, %v", audioID, err)
	}
	batchID, err := generator.NewGenerationBatchID()
	if err != nil || batchID.Validate() != nil {
		t.Fatalf("NewGenerationBatchID() = %q, %v", batchID, err)
	}
	jobID, err := generator.NewGenerationJobID()
	if err != nil || jobID.Validate() != nil {
		t.Fatalf("NewGenerationJobID() = %q, %v", jobID, err)
	}
	exportID, err := generator.NewExportJobID()
	if err != nil || exportID.Validate() != nil {
		t.Fatalf("NewExportJobID() = %q, %v", exportID, err)
	}
}

func TestSecureIDGeneratorReportsRandomSourceFailure(t *testing.T) {
	t.Parallel()

	generator := SecureIDGenerator{reader: bytes.NewReader(nil)}
	if _, err := generator.NewProjectID(); err == nil {
		t.Fatal("NewProjectID() error = nil, want random source error")
	}
	if _, err := (SecureIDGenerator{}).NewEpisodeID(); err == nil {
		t.Fatal("NewEpisodeID() with nil reader error = nil, want error")
	}
	if _, err := (SecureIDGenerator{}).NewAssetID(); err == nil {
		t.Fatal("NewAssetID() with nil reader error = nil, want error")
	}
	if _, err := (SecureIDGenerator{}).NewAssetIdentityID(); err == nil {
		t.Fatal("NewAssetIdentityID() with nil reader error = nil, want error")
	}
	if _, err := (SecureIDGenerator{}).NewSceneID(); err == nil {
		t.Fatal("NewSceneID() with nil reader error = nil, want error")
	}
	if _, err := (SecureIDGenerator{}).NewClipID(); err == nil {
		t.Fatal("NewClipID() with nil reader error = nil, want error")
	}
	if _, err := (SecureIDGenerator{}).NewArtifactID(); err == nil {
		t.Fatal("NewArtifactID() with nil reader error = nil, want error")
	}
	if _, err := (SecureIDGenerator{}).NewMediaVersionID(); err == nil {
		t.Fatal("NewMediaVersionID() with nil reader error = nil, want error")
	}
	if _, err := (SecureIDGenerator{}).NewAudioReferenceAssetID(); err == nil {
		t.Fatal("NewAudioReferenceAssetID() with nil reader error = nil, want error")
	}
	if _, err := (SecureIDGenerator{}).NewGenerationBatchID(); err == nil {
		t.Fatal("NewGenerationBatchID() with nil reader error = nil, want error")
	}
	if _, err := (SecureIDGenerator{}).NewGenerationJobID(); err == nil {
		t.Fatal("NewGenerationJobID() with nil reader error = nil, want error")
	}
	if _, err := (SecureIDGenerator{}).NewExportJobID(); err == nil {
		t.Fatal("NewExportJobID() with nil reader error = nil, want error")
	}
}

func TestUTCClockReturnsNormalizedTime(t *testing.T) {
	t.Parallel()

	value := (UTCClock{}).Now()
	if value.IsZero() || value.Location() != time.UTC || value.Nanosecond()%int(time.Millisecond) != 0 {
		t.Fatalf("UTCClock.Now() = %v, want non-zero UTC millisecond timestamp", value)
	}
	if _, err := validOperationTime(value); err != nil {
		t.Fatalf("validOperationTime() error = %v", err)
	}
	if _, err := validOperationTime(time.Time{}); err == nil {
		t.Fatalf("validOperationTime(zero) error = %v, want Clock validation error", err)
	}
}
