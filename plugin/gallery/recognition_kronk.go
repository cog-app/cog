package gallery

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/ardanlabs/kronk/sdk/kronk"
	"github.com/ardanlabs/kronk/sdk/kronk/jsonrepair"
	"github.com/ardanlabs/kronk/sdk/kronk/model"
	"github.com/ardanlabs/kronk/sdk/tools/libs"
	"github.com/ardanlabs/kronk/sdk/tools/models"
)

// KronkBackend runs a vision language model in-process via kronk (llama.cpp
// through yzma/purego). Unlike OllamaBackend it needs no external daemon: the
// llama.cpp shared libraries and the GGUF model are downloaded on first use
// into the plugin data directory, the same way OnnxBackend fetches its models.
//
// It produces tags and a scene description only. Face detection is not
// possible here — SCRFD boxes and ArcFace embeddings come from OnnxBackend.
type KronkBackend struct {
	// ModelSource is a HuggingFace URL, a "provider/modelID", or a bare
	// model id, e.g. "unsloth/Qwen3.5-0.8B-Q8_0". The companion mmproj is
	// auto-discovered for ids.
	ModelSource string
	// CacheDir holds downloaded llama.cpp libs and GGUF files.
	CacheDir string
	// Processor forces a llama.cpp build ("cpu", "cuda", "vulkan", "metal",
	// "hip"). Empty auto-detects. Useful when detection declines a GPU another
	// backend can still drive — a pre-Turing NVIDIA card is rejected by CUDA 13
	// but may run under Vulkan. Forcing a GPU build whose memory the model does
	// not fit into aborts the process from inside llama.cpp, so auto-detect is
	// the safe default.
	Processor string

	// mu guards the fields below. It is held for the whole of Analyze: llama
	// contexts are not safe for concurrent use, and the memory watchdog can
	// call Release from another goroutine mid-scan.
	mu      sync.Mutex
	initErr error
	krn     *kronk.Kronk
}

func (b *KronkBackend) Name() string { return "Kronk (" + b.ModelSource + ")" }

func (b *KronkBackend) Available() bool { return b.ModelSource != "" }

// InitErr downloads libraries and the model, then loads it. Called eagerly by
// runRecognition so a broken configuration fails before the scan loop starts.
func (b *KronkBackend) InitErr() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.ensure(context.Background())
}

// ensure loads the model once. The caller must hold b.mu. A failure is sticky
// until Release so a broken configuration does not retry the download for
// every image in the scan.
func (b *KronkBackend) ensure(ctx context.Context) error {
	if b.krn != nil {
		return nil
	}
	if b.initErr != nil {
		return b.initErr
	}
	b.initErr = b.init(ctx)

	return b.initErr
}

// kronkLog adapts kronk's progress logging to slog. The default FmtLogger
// writes carriage-return progress bars straight to stdout, which does not
// belong in a server's output.
func kronkLog(ctx context.Context, msg string, args ...any) {
	msg = strings.TrimSpace(strings.TrimPrefix(msg, "\r"))
	if msg == "" {
		return
	}
	slog.InfoContext(ctx, "kronk: "+msg, args...)
}

func (b *KronkBackend) init(ctx context.Context) error {
	if !b.Available() {
		return fmt.Errorf("kronk backend not configured: no model set")
	}
	if err := os.MkdirAll(b.CacheDir, 0o755); err != nil {
		return fmt.Errorf("create kronk cache dir: %w", err)
	}

	// Detect the best backend for this machine (CUDA/Vulkan/Metal/CPU) and
	// download the matching llama.cpp shared libraries.
	lib, err := libs.New(
		libs.WithBasePath(b.CacheDir),
		libs.WithDetectOverrides(ctx, kronkLog, "", "", "", b.Processor),
	)
	if err != nil {
		return fmt.Errorf("detect llama.cpp libraries: %w", err)
	}
	if _, err := lib.Download(ctx, kronkLog); err != nil {
		return fmt.Errorf("download llama.cpp libraries: %w", err)
	}
	if err := kronk.Init(kronk.WithLibPath(lib.LibsPath())); err != nil {
		return fmt.Errorf("init kronk: %w", err)
	}

	mdls, err := models.NewWithPaths(b.CacheDir)
	if err != nil {
		return fmt.Errorf("init model store: %w", err)
	}
	mp, err := mdls.Download(ctx, kronkLog, b.ModelSource)
	if err != nil {
		return fmt.Errorf("download model %q: %w", b.ModelSource, err)
	}
	if mp.ProjFile == "" {
		return fmt.Errorf("model %q has no mmproj file; not a vision model", b.ModelSource)
	}

	krn, err := kronk.New(
		model.WithModelFiles(mp.ModelFiles),
		model.WithProjFile(mp.ProjFile),
		model.WithAutoTune(true),
	)
	if err != nil {
		return fmt.Errorf("load model %q: %w", b.ModelSource, err)
	}
	b.krn = krn

	return nil
}

// tagSchema constrains decoding so the model can only emit the JSON shape we
// parse. This is the main advantage over OllamaBackend, whose free-form output
// needs the brace-scanning fallback in parseOllamaResponse.
//
// The grammar cannot bound the array — kronk's schema-to-GBNF conversion
// ignores maxItems — so a chatty model can still run into the token limit
// mid-array. parseKronkResponse salvages the truncated prefix.
var tagSchema = model.D{
	"type": "json_schema",
	"json_schema": model.D{
		"name": "image_tags",
		"schema": model.D{
			"type": "object",
			"properties": model.D{
				"tags":  model.D{"type": "array", "items": model.D{"type": "string"}},
				"scene": model.D{"type": "string"},
			},
			"required": []string{"tags", "scene"},
		},
	},
}

const kronkPrompt = `Analyze this image. List at most 15 short tags describing objects, activities, colors, and setting visible in the image, plus a brief scene description. Do not repeat a tag.`

func (b *KronkBackend) Analyze(ctx context.Context, imagePath string) (*RecognitionResult, error) {
	imgData, err := os.ReadFile(imagePath)
	if err != nil {
		return nil, fmt.Errorf("read image: %w", err)
	}
	typ := strings.TrimPrefix(strings.ToLower(filepath.Ext(imagePath)), ".")
	if typ == "" {
		typ = "jpg"
	}

	d := model.D{
		"messages":        model.ImageMessage(kronkPrompt, imgData, typ),
		"response_format": tagSchema,
		"enable_thinking": false, // grammar must match from the first token
		"temperature":     0.3,
		"max_tokens":      1024,
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if err := b.ensure(ctx); err != nil {
		return nil, err
	}

	resp, err := b.krn.Chat(ctx, d)
	if err != nil {
		return nil, fmt.Errorf("kronk chat: %w", err)
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("kronk returned no choices")
	}

	msg := resp.Choices[0].Message
	if msg == nil {
		msg = resp.Choices[0].Delta
	}
	if msg == nil {
		return nil, fmt.Errorf("kronk returned an empty message")
	}

	return parseKronkResponse(msg.Content), nil
}

// maxKronkTags bounds how many tags one image contributes. The grammar cannot
// express maxItems, so a model that ignores the prompt is cut off here.
const maxKronkTags = 15

// parseKronkResponse turns the model's reply into tags.
//
// The grammar guarantees well-formed JSON right up to the point generation
// stops, so the happy path is a plain unmarshal. When the token limit lands
// mid-value the prefix is still valid, which is what the two fallbacks exploit:
// jsonrepair closes the open value, and failing that the tag strings are read
// straight out of the array prefix.
func parseKronkResponse(content string) *RecognitionResult {
	parsed, _ := unmarshalTags(content)
	if len(parsed.Tags) == 0 {
		// Repair can succeed while discarding the truncated array, so only
		// take its result when it actually recovered tags.
		if repaired, err := jsonrepair.Repair(content); err == nil {
			if repairedTags, _ := unmarshalTags(repaired); len(repairedTags.Tags) > 0 {
				parsed = repairedTags
			}
		}
	}
	if len(parsed.Tags) == 0 {
		parsed.Tags = tagsFromPrefix(content)
	}

	result := &RecognitionResult{}
	seen := make(map[string]struct{}, len(parsed.Tags))
	for _, tag := range parsed.Tags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		// A "tag" longer than a short phrase is the model narrating, not
		// labelling. Those belong in the scene description, if anywhere.
		if tag == "" || len(tag) > 50 {
			continue
		}
		if _, dup := seen[tag]; dup {
			continue
		}
		seen[tag] = struct{}{}
		result.Tags = append(result.Tags, RecognizedTag{Name: tag, Confidence: 0.8, Type: 1})
		if len(result.Tags) == maxKronkTags {
			break
		}
	}

	if scene := strings.TrimSpace(parsed.Scene); scene != "" {
		result.Tags = append(result.Tags, RecognizedTag{
			Name: strings.ToLower(scene), Confidence: 0.7, Type: 2,
		})
	}

	return result
}

// kronkTags mirrors the JSON schema in tagSchema.
type kronkTags struct {
	Tags  []string `json:"tags"`
	Scene string   `json:"scene"`
}

func unmarshalTags(content string) (kronkTags, bool) {
	content = strings.TrimSpace(content)
	if start, end := strings.Index(content, "{"), strings.LastIndex(content, "}"); start >= 0 && end > start {
		content = content[start : end+1]
	}

	var parsed kronkTags
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return kronkTags{}, false
	}

	return parsed, true
}

// tagsFromPrefix reads complete quoted strings out of a truncated "tags" array.
// Anything after the array closes is ignored, so scene text is never mistaken
// for a tag.
func tagsFromPrefix(content string) []string {
	start := strings.Index(content, `"tags"`)
	if start < 0 {
		return nil
	}
	offset := strings.Index(content[start:], "[")
	if offset < 0 {
		return nil
	}
	rest := content[start+offset:]
	if end := strings.Index(rest, "]"); end >= 0 {
		rest = rest[:end]
	}

	var tags []string
	for _, match := range quotedString.FindAllStringSubmatch(rest, -1) {
		var tag string
		// Reuse the JSON unquoting rules so escapes survive.
		if err := json.Unmarshal([]byte(match[0]), &tag); err != nil {
			continue
		}
		tags = append(tags, tag)
	}

	return tags
}

var quotedString = regexp.MustCompile(`"(?:[^"\\]|\\.)*"`)

// Release unloads the model, freeing the weights between scan passes.
func (b *KronkBackend) Release() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.krn == nil {
		return
	}
	if err := b.krn.Unload(context.Background()); err != nil {
		// Caller has no error path; log rather than swallow.
		slog.Error("kronk unload", "err", err)
	}
	b.krn = nil
	b.initErr = nil
}
