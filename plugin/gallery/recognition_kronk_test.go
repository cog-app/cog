package gallery

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func TestParseKronkResponse(t *testing.T) {
	tests := []struct {
		name    string
		content string
		tags    []string
		scene   string
	}{
		{
			name:    "well formed",
			content: `{"tags": ["Tree", " soil ", "blue"], "scene": "A Minimalist Illustration."}`,
			tags:    []string{"tree", "soil", "blue"},
			scene:   "a minimalist illustration.",
		},
		{
			name:    "prose around the object",
			content: "Here you go:\n```json\n{\"tags\": [\"tree\"], \"scene\": \"a tree\"}\n```",
			tags:    []string{"tree"},
			scene:   "a tree",
		},
		{
			// Generation stopped mid-string: everything before it survives.
			name:    "truncated mid array",
			content: "{\n  \"tags\": [\n    \"plant\",\n    \"book\",\n    \"gre",
			tags:    []string{"plant", "book"},
		},
		{
			// Truncated after the array closed but before scene was written.
			name:    "truncated after array",
			content: `{"tags": ["plant", "book"], "sce`,
			tags:    []string{"plant", "book"},
		},
		{
			name:    "duplicates and sentences dropped",
			content: `{"tags": ["tree", "TREE", "` + strings.Repeat("x", 51) + `", "soil"], "scene": ""}`,
			tags:    []string{"tree", "soil"},
		},
		{
			name:    "no tags at all",
			content: "I cannot help with that.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseKronkResponse(tt.content)

			var tags []string
			var scene string
			for _, tag := range result.Tags {
				switch tag.Type {
				case 1:
					tags = append(tags, tag.Name)
				case 2:
					scene = tag.Name
				}
			}

			assert.Equal(t, tt.tags, tags)
			assert.Equal(t, tt.scene, scene)
		})
	}
}

func TestParseKronkResponseCapsTags(t *testing.T) {
	var quoted []string
	for i := range maxKronkTags + 10 {
		quoted = append(quoted, `"tag`+string(rune('a'+i))+`"`)
	}
	content := `{"tags": [` + strings.Join(quoted, ", ") + `], "scene": "s"}`

	result := parseKronkResponse(content)
	require.Equal(t, maxKronkTags+1, len(result.Tags)) // tags + scene
}

// TestKronkBackendAnalyze exercises the real Kronk backend end to end. It is
// skipped unless KRONK_TEST_IMAGE is set, because the first run downloads the
// llama.cpp shared libraries and several hundred MB of model weights.
//
//	KRONK_TEST_IMAGE=/path/to/photo.jpg go test -run TestKronkBackendAnalyze ./plugin/gallery
//
// KRONK_TEST_MODEL overrides the model, KRONK_TEST_CACHE the download
// directory (defaults to the kronk system default under $HOME), and
// KRONK_TEST_PROCESSOR the llama.cpp build (cpu, cuda, vulkan, ...).
func TestKronkBackendAnalyze(t *testing.T) {
	imagePath := os.Getenv("KRONK_TEST_IMAGE")
	if imagePath == "" {
		t.Skip("KRONK_TEST_IMAGE not set")
	}

	modelSource := os.Getenv("KRONK_TEST_MODEL")
	if modelSource == "" {
		modelSource = "unsloth/Qwen3.5-0.8B-Q8_0"
	}

	b := &KronkBackend{
		ModelSource: modelSource,
		CacheDir:    os.Getenv("KRONK_TEST_CACHE"),
		Processor:   os.Getenv("KRONK_TEST_PROCESSOR"),
	}
	require.NoError(t, b.InitErr())
	defer b.Release()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	defer cancel()

	result, err := b.Analyze(ctx, imagePath)
	require.NoError(t, err)
	require.True(t, len(result.Tags) > 0, "expected at least one tag")

	for _, tag := range result.Tags {
		t.Logf("tag=%q confidence=%.2f type=%d", tag.Name, tag.Confidence, tag.Type)
	}
}
