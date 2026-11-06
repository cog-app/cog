package gallery

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// OllamaBackend uses an Ollama vision model for image tagging.
type OllamaBackend struct {
	URL   string // e.g. "http://localhost:11434"
	Model string // e.g. "llava:13b", "moondream"
}

func (b *OllamaBackend) Name() string { return "Ollama (" + b.Model + ")" }

func (b *OllamaBackend) Available() bool {
	return b.URL != "" && b.Model != ""
}

func (b *OllamaBackend) Analyze(ctx context.Context, imagePath string) (*RecognitionResult, error) {
	imgData, err := os.ReadFile(imagePath)
	if err != nil {
		return nil, fmt.Errorf("read image: %w", err)
	}
	imgB64 := base64.StdEncoding.EncodeToString(imgData)

	prompt := `Analyze this image. Respond with ONLY a JSON object, no other text:
{"tags": ["tag1", "tag2", ...], "scene": "brief scene description"}
List 5-15 tags describing objects, activities, colors, and setting visible in the image.`

	reqBody := map[string]any{
		"model":  b.Model,
		"prompt": prompt,
		"images": []string{imgB64},
		"stream": false,
		"options": map[string]any{
			"temperature": 0.3,
		},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("encode ollama request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", b.URL+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	// trusted URL: an admin sets it, and it is usually on localhost.
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama status %d", resp.StatusCode)
	}

	var ollamaResp struct {
		Response string `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ollamaResp); err != nil {
		return nil, fmt.Errorf("decode ollama response: %w", err)
	}

	return parseOllamaResponse(ollamaResp.Response), nil
}

// parseOllamaResponse extracts tags from the model's JSON response.
func parseOllamaResponse(response string) *RecognitionResult {
	result := &RecognitionResult{}

	// Try to extract JSON from the response.
	response = strings.TrimSpace(response)

	// Find JSON object in the response.
	start := strings.Index(response, "{")
	end := strings.LastIndex(response, "}")
	if start >= 0 && end > start {
		response = response[start : end+1]
	}

	var parsed struct {
		Tags  []string `json:"tags"`
		Scene string   `json:"scene"`
	}
	if err := json.Unmarshal([]byte(response), &parsed); err != nil {
		// Fallback: split on commas/newlines.
		for line := range strings.SplitSeq(response, "\n") {
			line = strings.TrimSpace(line)
			line = strings.Trim(line, "-*•")
			line = strings.TrimSpace(line)
			if line != "" && len(line) < 50 {
				result.Tags = append(result.Tags, RecognizedTag{
					Name: strings.ToLower(line), Confidence: 0.6, Type: 1,
				})
			}
		}
		return result
	}

	for _, tag := range parsed.Tags {
		tag = strings.TrimSpace(strings.ToLower(tag))
		if tag != "" {
			result.Tags = append(result.Tags, RecognizedTag{
				Name: tag, Confidence: 0.8, Type: 1,
			})
		}
	}
	if parsed.Scene != "" {
		result.Tags = append(result.Tags, RecognizedTag{
			Name: strings.ToLower(parsed.Scene), Confidence: 0.7, Type: 2,
		})
	}

	return result
}
