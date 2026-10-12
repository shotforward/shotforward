package project

import (
	"errors"
	"fmt"
)

var ErrInvalidOutputSpec = errors.New("invalid project output specification")

// OutputSpec is a fixed project-wide output size. Episode and Clip cannot
// override it.
type OutputSpec struct {
	width  int
	height int
}

// NewOutputSpec accepts only the product's supported portrait, landscape, and
// square 480p, 720p, and 1080p sizes.
func NewOutputSpec(width, height int) (OutputSpec, error) {
	spec := OutputSpec{width: width, height: height}
	if !spec.IsValid() {
		return OutputSpec{}, fmt.Errorf("%w: %dx%d is not supported", ErrInvalidOutputSpec, width, height)
	}
	return spec, nil
}

func (s OutputSpec) Width() int {
	return s.width
}

func (s OutputSpec) Height() int {
	return s.height
}

func (s OutputSpec) IsValid() bool {
	switch [2]int{s.width, s.height} {
	case
		[2]int{480, 854},
		[2]int{720, 1280},
		[2]int{1080, 1920},
		[2]int{854, 480},
		[2]int{1280, 720},
		[2]int{1920, 1080},
		[2]int{480, 480},
		[2]int{720, 720},
		[2]int{1080, 1080}:
		return true
	default:
		return false
	}
}
