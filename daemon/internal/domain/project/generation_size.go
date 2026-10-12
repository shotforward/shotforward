package project

const generatedAssetLongEdge = 1536

// GenerationOutputSize returns the required output size for a generation
// target. Timeline media stays at the exact project output size, while a
// project asset uses the same aspect ratio on a 1.5K canvas.
func GenerationOutputSize(kind MediaSlotKind, output OutputSpec) (int, int) {
	projectWidth := output.Width()
	projectHeight := output.Height()
	if kind != MediaSlotAssetPrimaryImage {
		return projectWidth, projectHeight
	}
	if projectWidth == projectHeight {
		return generatedAssetLongEdge, generatedAssetLongEdge
	}
	if projectWidth > projectHeight {
		return generatedAssetLongEdge, roundedGenerationMultipleOf16(generatedAssetLongEdge * projectHeight / projectWidth)
	}
	return roundedGenerationMultipleOf16(generatedAssetLongEdge * projectWidth / projectHeight), generatedAssetLongEdge
}

func roundedGenerationMultipleOf16(value int) int {
	return ((value + 8) / 16) * 16
}
