package project

import (
	"errors"
	"testing"
)

func TestSupportedOutputSpecs(t *testing.T) {
	t.Parallel()

	supported := [][2]int{
		{480, 854}, {720, 1280}, {1080, 1920},
		{854, 480}, {1280, 720}, {1920, 1080},
		{480, 480}, {720, 720}, {1080, 1080},
	}
	for _, size := range supported {
		spec, err := NewOutputSpec(size[0], size[1])
		if err != nil {
			t.Errorf("NewOutputSpec(%d, %d) error = %v", size[0], size[1], err)
			continue
		}
		if spec.Width() != size[0] || spec.Height() != size[1] || !spec.IsValid() {
			t.Errorf("NewOutputSpec(%d, %d) = %dx%d, valid %t", size[0], size[1], spec.Width(), spec.Height(), spec.IsValid())
		}
	}
}

func TestOutputSpecRejectsUnsupportedSize(t *testing.T) {
	t.Parallel()

	for _, size := range [][2]int{{0, 0}, {1920, 1081}, {720, 854}, {3840, 2160}} {
		if _, err := NewOutputSpec(size[0], size[1]); !errors.Is(err, ErrInvalidOutputSpec) {
			t.Errorf("NewOutputSpec(%d, %d) error = %v, want ErrInvalidOutputSpec", size[0], size[1], err)
		}
	}
}
