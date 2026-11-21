package gallery

import (
	"os"
	"strings"
)

// DeviceCUDA runs ONNX models on an NVIDIA GPU.
const DeviceCUDA = "cuda"

// onnxDevices is where ONNX models run: COG_ONNX_DEVICE, a property of the
// machine rather than of the server's settings, since a worker and its
// server have different hardware. It is a list: a worker runs an engine for
// each entry, so "cuda,cuda,cpu" keeps a GPU busy while one engine prepares
// its next photo, and puts the spare cores to work too. Anything but "cuda"
// is the CPU.
func onnxDevices() []string {
	var out []string
	for d := range strings.SplitSeq(os.Getenv("COG_ONNX_DEVICE"), ",") {
		if d = strings.TrimSpace(d); d != DeviceCUDA {
			d = ""
		}
		out = append(out, d)
	}
	return out
}

// onnxLibrary is COG_ONNX_LIBRARY, for a worker whose onnxruntime is not in
// a system directory, such as the GPU build beside its CUDA libraries.
func onnxLibrary() string { return os.Getenv("COG_ONNX_LIBRARY") }

const (
	mobilenetFile  = "mobilenetv2-12-int8.onnx"
	joytagFile     = "joytag.onnx"
	joytagTagsFile = "joytag_tags.txt"
	wd14File       = "wd14.onnx"
	wd14TagsFile   = "wd14_tags.csv"
	arcfaceFile    = "w600k_r50.onnx"
	scrfdFile      = "det_10g.onnx"
	genderageFile  = "genderage.onnx"
)

// onnxModelFiles are the files the ONNX backend loads with a tag model:
// what a remote worker must have, byte for byte, to find what the server
// would.
func onnxModelFiles(tag TagModel) []string {
	files := []string{arcfaceFile, scrfdFile, genderageFile}
	switch tag {
	case TagModelJoyTag:
		return append(files, joytagFile, joytagTagsFile)
	case TagModelWD14:
		return append(files, wd14File, wd14TagsFile)
	default:
		return append(files, mobilenetFile)
	}
}
