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

// OpenAIBackend uses an OpenAI Vision-compatible API for image tagging.
type OpenAIBackend struct {
	URL    string // e.g. "https://api.openai.com/v1" or local vLLM
	APIKey string
	Model  string // e.g. "gpt-4o-mini"
}

func (b *OpenAIBackend) Name() string { return "OpenAI (" + b.Model + ")" }

func (b *OpenAIBackend) Available() bool {
	return b.URL != "" && b.APIKey != "" && b.Model != ""
}

func (b *OpenAIBackend) Analyze(ctx context.Context, imagePath string) (*RecognitionResult, error) {
	imgData, err := os.ReadFile(imagePath)
	if err != nil {
		return nil, fmt.Errorf("read image: %w", err)
	}
	imgB64 := base64.StdEncoding.EncodeToString(imgData)

	// Detect MIME type from extension.
	mimeType := "image/jpeg"
	if strings.HasSuffix(strings.ToLower(imagePath), ".png") {
		mimeType = "image/png"
	} else if strings.HasSuffix(strings.ToLower(imagePath), ".webp") {
		mimeType = "image/webp"
	}

	reqBody := map[string]any{
		"model": b.Model,
		"messages": []map[string]any{
			{
				"role": "user",
				"content": []map[string]any{
					{
						"type": "text",
						"text": `Analyze this image. Respond with ONLY a JSON object:
{"tags": ["tag1", "tag2", ...], "scene": "brief scene description"}
List 5-15 tags describing objects, activities, colors, and setting.`,
					},
					{
						"type": "image_url",
						"image_url": map[string]string{
							"url": fmt.Sprintf("data:%s;base64,%s", mimeType, imgB64),
						},
					},
				},
			},
		},
		"max_tokens": 300,
	}
	body, _ := json.Marshal(reqBody)

	url := strings.TrimSuffix(b.URL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+b.APIKey)

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openai status %d", resp.StatusCode)
	}

	var chatResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return nil, fmt.Errorf("decode openai response: %w", err)
	}

	if len(chatResp.Choices) == 0 {
		return &RecognitionResult{}, nil
	}

	return parseOllamaResponse(chatResp.Choices[0].Message.Content), nil
}
