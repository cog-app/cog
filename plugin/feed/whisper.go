package feed

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"git.duckfam.us/jj-software/cog/internal/safehttp"
)

var whisperModelFiles = map[WhisperModel]string{
	WhisperTinyEn:   "ggml-tiny.en.bin",
	WhisperBaseEn:   "ggml-base.en.bin",
	WhisperSmallEn:  "ggml-small.en.bin",
	WhisperMediumEn: "ggml-medium.en.bin",
	WhisperLargeV3:  "ggml-large-v3.bin",
}

// modelHTTP downloads models. Their URLs are constants, but the models are
// large, so the client has no overall timeout; safehttp's idle timeout still
// gives up on a server that stops sending.
var modelHTTP = safehttp.Client(0)

const whisperModelBaseURL = "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/"

// WhisperModelFile returns the filename for a given model, or "" if the model
// is not recognized (e.g. "none").
func WhisperModelFile(m WhisperModel) string {
	return whisperModelFiles[m]
}

// EnsureWhisperModel downloads the model file to modelsDir if it's not already
// present, and returns its absolute path. modelsDir is created if it does not
// exist. Returns an error and an empty path for unrecognized models.
func EnsureWhisperModel(modelsDir string, model WhisperModel) (string, error) {
	file, ok := whisperModelFiles[model]
	if !ok {
		return "", fmt.Errorf("unknown whisper model %q", model)
	}
	if err := os.MkdirAll(modelsDir, 0o755); err != nil {
		return "", fmt.Errorf("create models dir: %w", err)
	}
	path := filepath.Join(modelsDir, file)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	url := whisperModelBaseURL + file
	slog.Info("feed: downloading whisper model", "model", string(model), "url", url, "path", path)
	resp, err := modelHTTP.Get(url)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: status %d", url, resp.StatusCode)
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return "", fmt.Errorf("create tmp: %w", err)
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		return "", fmt.Errorf("copy: %w", errors.Join(err, f.Close(), os.Remove(tmp)))
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("close tmp: %w", errors.Join(err, os.Remove(tmp)))
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", fmt.Errorf("rename: %w", err)
	}
	return path, nil
}
