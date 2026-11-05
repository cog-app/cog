package gallery

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/sandbox"
)

var tesseractPath string

func init() {
	tesseractPath, _ = exec.LookPath("tesseract")
}

// TesseractAvailable reports whether tesseract is installed.
func TesseractAvailable() bool {
	return tesseractPath != ""
}

// tesseractOCR runs tesseract on an image file and returns the extracted text.
func tesseractOCR(imagePath string) (string, error) {
	if !TesseractAvailable() {
		return "", fmt.Errorf("tesseract not available")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// tesseract <input> stdout -l eng --psm 3
	cmd := sandbox.CommandContext(ctx, sandbox.Spec{Read: []string{imagePath}}, tesseractPath, imagePath, "stdout", "-l", "eng", "--psm", "3")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("tesseract: %w", err)
	}

	text := strings.TrimSpace(string(out))
	// Filter out noise — tesseract on photos often returns garbage.
	if len(text) < 3 || isGarbage(text) {
		return "", nil
	}
	return text, nil
}

// isGarbage returns true if the OCR text looks like noise rather than real text.
func isGarbage(text string) bool {
	// Count alphanumeric vs non-alphanumeric ratio.
	var alnum, total int
	for _, r := range text {
		total++
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == ' ' {
			alnum++
		}
	}
	if total == 0 {
		return true
	}
	// If less than 50% of characters are alphanumeric+space, it's likely noise.
	return float64(alnum)/float64(total) < 0.5
}
