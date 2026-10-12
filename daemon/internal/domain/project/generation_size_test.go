package project

import "testing"

func TestGenerationOutputSizeUsesOnePointFiveKForAssetsOnly(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		kind       MediaSlotKind
		width      int
		height     int
		wantWidth  int
		wantHeight int
	}{
		{name: "portrait asset", kind: MediaSlotAssetPrimaryImage, width: 720, height: 1280, wantWidth: 864, wantHeight: 1536},
		{name: "480p portrait asset", kind: MediaSlotAssetPrimaryImage, width: 480, height: 854, wantWidth: 864, wantHeight: 1536},
		{name: "landscape asset", kind: MediaSlotAssetPrimaryImage, width: 1920, height: 1080, wantWidth: 1536, wantHeight: 864},
		{name: "square asset", kind: MediaSlotAssetPrimaryImage, width: 720, height: 720, wantWidth: 1536, wantHeight: 1536},
		{name: "first frame", kind: MediaSlotClipFirstImage, width: 720, height: 1280, wantWidth: 720, wantHeight: 1280},
		{name: "last frame", kind: MediaSlotClipLastImage, width: 1920, height: 1080, wantWidth: 1920, wantHeight: 1080},
		{name: "video", kind: MediaSlotClipVideo, width: 1280, height: 720, wantWidth: 1280, wantHeight: 720},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			output, err := NewOutputSpec(test.width, test.height)
			if err != nil {
				t.Fatal(err)
			}
			width, height := GenerationOutputSize(test.kind, output)
			if width != test.wantWidth || height != test.wantHeight {
				t.Fatalf("GenerationOutputSize() = %dx%d, want %dx%d", width, height, test.wantWidth, test.wantHeight)
			}
		})
	}
}
