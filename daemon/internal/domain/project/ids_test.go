package project

import (
	"errors"
	"testing"
)

func TestTypedIDs(t *testing.T) {
	t.Parallel()

	projectValue := "prj_ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	projectID, err := ParseProjectID(projectValue)
	if err != nil {
		t.Fatalf("ParseProjectID() error = %v", err)
	}
	if projectID.String() != projectValue {
		t.Fatalf("ProjectID.String() = %q, want %q", projectID, projectValue)
	}

	episodeValue := "ep_234567ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	episodeID, err := ParseEpisodeID(episodeValue)
	if err != nil {
		t.Fatalf("ParseEpisodeID() error = %v", err)
	}
	if episodeID.String() != episodeValue {
		t.Fatalf("EpisodeID.String() = %q, want %q", episodeID, episodeValue)
	}

	assetValue := "ast_ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	assetID, err := ParseAssetID(assetValue)
	if err != nil || assetID.String() != assetValue {
		t.Fatalf("ParseAssetID(%q) = %q, %v", assetValue, assetID, err)
	}
	identityValue := "asi_234567ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	identityID, err := ParseAssetIdentityID(identityValue)
	if err != nil || identityID.String() != identityValue {
		t.Fatalf("ParseAssetIdentityID(%q) = %q, %v", identityValue, identityID, err)
	}
	sceneValue := "scn_ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	sceneID, err := ParseSceneID(sceneValue)
	if err != nil || sceneID.String() != sceneValue {
		t.Fatalf("ParseSceneID(%q) = %q, %v", sceneValue, sceneID, err)
	}
	clipValue := "clp_234567ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	clipID, err := ParseClipID(clipValue)
	if err != nil || clipID.String() != clipValue {
		t.Fatalf("ParseClipID(%q) = %q, %v", clipValue, clipID, err)
	}
	batchValue := "gbt_ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	batchID, err := ParseGenerationBatchID(batchValue)
	if err != nil || batchID.String() != batchValue {
		t.Fatalf("ParseGenerationBatchID(%q) = %q, %v", batchValue, batchID, err)
	}
	jobValue := "gjb_234567ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	jobID, err := ParseGenerationJobID(jobValue)
	if err != nil || jobID.String() != jobValue {
		t.Fatalf("ParseGenerationJobID(%q) = %q, %v", jobValue, jobID, err)
	}
}

func TestTypedIDsRejectInvalidValues(t *testing.T) {
	t.Parallel()

	projectTests := []string{
		"",
		"ep_ABCDEFGHIJKLMNOPQRSTUVWXYZ",
		"prj_SHORT",
		"prj_abcdefghijklmnopqrstuvwxzy",
		"prj_ABCDEFGHIJKLMNOPQRSTUVWXYZ!",
	}
	for _, value := range projectTests {
		if _, err := ParseProjectID(value); !errors.Is(err, ErrInvalidProjectID) {
			t.Errorf("ParseProjectID(%q) error = %v, want ErrInvalidProjectID", value, err)
		}
	}

	episodeTests := []string{
		"",
		"prj_ABCDEFGHIJKLMNOPQRSTUVWXYZ",
		"ep_SHORT",
		"ep_00000000000000000000000000",
	}
	for _, value := range episodeTests {
		if _, err := ParseEpisodeID(value); !errors.Is(err, ErrInvalidEpisodeID) {
			t.Errorf("ParseEpisodeID(%q) error = %v, want ErrInvalidEpisodeID", value, err)
		}
	}
	if _, err := ParseAssetID("asi_ABCDEFGHIJKLMNOPQRSTUVWXYZ"); !errors.Is(err, ErrInvalidAssetID) {
		t.Errorf("ParseAssetID() error = %v, want ErrInvalidAssetID", err)
	}
	if _, err := ParseAssetIdentityID("ast_ABCDEFGHIJKLMNOPQRSTUVWXYZ"); !errors.Is(err, ErrInvalidAssetIdentityID) {
		t.Errorf("ParseAssetIdentityID() error = %v, want ErrInvalidAssetIdentityID", err)
	}
	if _, err := ParseSceneID("clp_ABCDEFGHIJKLMNOPQRSTUVWXYZ"); !errors.Is(err, ErrInvalidSceneID) {
		t.Errorf("ParseSceneID() error = %v, want ErrInvalidSceneID", err)
	}
	if _, err := ParseClipID("scn_ABCDEFGHIJKLMNOPQRSTUVWXYZ"); !errors.Is(err, ErrInvalidClipID) {
		t.Errorf("ParseClipID() error = %v, want ErrInvalidClipID", err)
	}
	if _, err := ParseGenerationBatchID("gjb_ABCDEFGHIJKLMNOPQRSTUVWXYZ"); !errors.Is(err, ErrInvalidGenerationBatchID) {
		t.Errorf("ParseGenerationBatchID() error = %v, want ErrInvalidGenerationBatchID", err)
	}
	if _, err := ParseGenerationJobID("gbt_ABCDEFGHIJKLMNOPQRSTUVWXYZ"); !errors.Is(err, ErrInvalidGenerationJobID) {
		t.Errorf("ParseGenerationJobID() error = %v, want ErrInvalidGenerationJobID", err)
	}
}
