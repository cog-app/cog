package gallery

import "os"

// DeviceCUDA runs ONNX models on an NVIDIA GPU.
const DeviceCUDA = "cuda"

// onnxDevice is where ONNX models run: COG_ONNX_DEVICE, a property of the
// machine rather than of the server's settings, since a worker and its
// server have different hardware.
func onnxDevice() string { return os.Getenv("COG_ONNX_DEVICE") }

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
