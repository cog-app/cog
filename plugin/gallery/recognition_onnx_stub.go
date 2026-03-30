//go:build !linux

package gallery

import (
	"context"
	"fmt"
)

// OnnxBackend is a stub for non-Linux platforms where ONNX runtime is unavailable.
type OnnxBackend struct {
	ModelsDir   string
	LibraryPath string
	TagModel    TagModel
}

func (b *OnnxBackend) Name() string    { return "ONNX (local)" }
func (b *OnnxBackend) Available() bool { return false }
func (b *OnnxBackend) Release()        {}
func (b *OnnxBackend) InitErr() error {
	return fmt.Errorf("onnx runtime is not supported on this platform")
}
func (b *OnnxBackend) Analyze(ctx context.Context, imagePath string) (*RecognitionResult, error) {
	return nil, fmt.Errorf("onnx runtime is not supported on this platform")
}
