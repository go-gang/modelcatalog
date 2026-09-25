// Package cataloggen builds the offline catalog from pinned public metadata.
// It is used by the maintenance command and tests, never by runtime discovery.
package cataloggen

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-gang/modelcatalog/internal/modelcatalog"
)

// OpenRouterSource is public model metadata, not an inference endpoint.
const OpenRouterSource = "https://openrouter.ai/api/v1/models"

type sourceModel struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Architecture struct {
		Input     []string `json:"input_modalities"`
		Output    []string `json:"output_modalities"`
		Tokenizer string   `json:"tokenizer"`
	} `json:"architecture"`
	ContextLength *int64 `json:"context_length"`
	TopProvider   struct {
		ContextLength *int64 `json:"context_length"`
		MaxOutput     *int64 `json:"max_completion_tokens"`
	} `json:"top_provider"`
	Parameters []string                   `json:"supported_parameters"`
	Pricing    map[string]json.RawMessage `json:"pricing"`
	Expiration *string                    `json:"expiration_date"`
}

// Report records exact inclusion and exclusion decisions for a pinned input.
type Report struct {
	Included []string            `json:"included"`
	Excluded map[string][]string `json:"excluded"`
}

// Build combines reviewed records with compatible OpenRouter entries. Reviewed
// OpenRouter entries replace generated records only for the same literal ID.
// retrievedDate fixes expiration checks; revision is saved with every quote.
func Build(input []byte, reviewed modelcatalog.Layer, retrievedDate, revision string) ([]byte, Report, error) {
	report := Report{Excluded: map[string][]string{}}
	date, err := time.Parse(time.DateOnly, retrievedDate)
	if err != nil || strings.TrimSpace(revision) == "" {
		return nil, report, fmt.Errorf("a retrieval date and snapshot revision are required")
	}
	if err := reviewed.Validate(); err != nil {
		return nil, report, err
	}
	var source struct {
		Models []sourceModel `json:"data"`
		Total  *int          `json:"total_count"`
		Links  struct {
			Next *string `json:"next"`
		} `json:"links"`
	}
	if err := json.Unmarshal(input, &source); err != nil {
		return nil, report, fmt.Errorf("decode OpenRouter input: %w", err)
	}
	if len(source.Models) == 0 || (source.Total != nil && *source.Total != len(source.Models)) || source.Links.Next != nil {
		return nil, report, fmt.Errorf("OpenRouter input must contain a complete nonempty snapshot")
	}
	overrides := map[string]modelcatalog.Entry{}
	layer := modelcatalog.Layer{Version: modelcatalog.Version}
	for _, entry := range reviewed.Models {
		if entry.Provider == modelcatalog.ProviderOpenRouter {
			overrides[entry.ID] = entry.Clone()
		} else {
			layer.Models = append(layer.Models, entry.Clone())
		}
	}
	seen := map[string]bool{}
	for _, raw := range source.Models {
		if raw.ID == "" || seen[raw.ID] {
			return nil, report, fmt.Errorf("empty or duplicate source model ID %q", raw.ID)
		}
		seen[raw.ID] = true
		reason, err := exclude(raw, date)
		if err != nil {
			return nil, report, err
		}
		if reason != "" {
			report.Excluded[reason] = append(report.Excluded[reason], raw.ID)

			continue
		}
		entry := project(raw, revision)
		if override, ok := overrides[raw.ID]; ok {
			entry = override
			delete(overrides, raw.ID)
		}
		layer.Models = append(layer.Models, entry)
		report.Included = append(report.Included, raw.ID)
	}
	if len(overrides) != 0 {
		return nil, report, fmt.Errorf("reviewed OpenRouter record is absent or incompatible in the source")
	}
	slices.SortFunc(layer.Models, func(a, b modelcatalog.Entry) int {
		if order := strings.Compare(a.Provider, b.Provider); order != 0 {
			return order
		}

		return strings.Compare(a.ID, b.ID)
	})
	if _, err := modelcatalog.Merge(layer); err != nil {
		return nil, report, fmt.Errorf("generated catalog: %w", err)
	}
	slices.Sort(report.Included)
	for _, ids := range report.Excluded {
		slices.Sort(ids)
	}
	output, err := json.MarshalIndent(layer, "", "  ")
	if err != nil {
		return nil, report, err
	}

	return append(output, '\n'), report, nil
}

func exclude(raw sourceModel, date time.Time) (string, error) {
	if !slices.Contains(raw.Architecture.Input, "text") || !slices.Equal(raw.Architecture.Output, []string{"text"}) {
		return "unsupported_modalities", nil
	}
	if !slices.Contains(raw.Parameters, "tools") || !slices.Contains(raw.Parameters, "tool_choice") {
		return "tools_and_tool_choice_required", nil
	}
	// OpenRouter documents :batch as a catalog variant served by its Batch API.
	// This is a transport distinction, not a model-family capability heuristic.
	if slices.Contains(strings.Split(raw.ID, ":")[1:], "batch") {
		return "batch_api", nil
	}
	if raw.Architecture.Tokenizer == "Router" {
		return "dynamic_router", nil
	}
	if !slices.Contains(raw.Parameters, "max_tokens") && !slices.Contains(raw.Parameters, "max_completion_tokens") {
		return "output_limit_parameter_unknown", nil
	}
	if raw.Expiration != nil {
		expiry, err := time.Parse(time.DateOnly, *raw.Expiration)
		if err != nil {
			return "", fmt.Errorf("model %q has an invalid expiration date", raw.ID)
		}
		if !expiry.After(date) {
			return "expired", nil
		}
	}

	return "", nil
}

func project(raw sourceModel, revision string) modelcatalog.Entry {
	// System messages and streaming are OpenRouter Chat transport contracts;
	// tool/image support comes from the exact model record, never its name.
	capabilities := modelcatalog.Capabilities{
		Tools: modelcatalog.Known(true), System: modelcatalog.Known(true), Streaming: modelcatalog.Known(true),
		Images:          modelcatalog.Known(slices.Contains(raw.Architecture.Input, "image")),
		Reasoning:       modelcatalog.Known(slices.Contains(raw.Parameters, "reasoning")),
		ReasoningLevels: modelcatalog.Unknown[[]string](),
		ContextWindow:   positiveMinimum(raw.ContextLength, raw.TopProvider.ContextLength),
		MaxOutputTokens: positiveMinimum(raw.TopProvider.MaxOutput),
	}
	maxTokens := modelcatalog.MaxTokensLegacy
	if !slices.Contains(raw.Parameters, "max_tokens") {
		maxTokens = modelcatalog.MaxTokensCompletion
	}

	return modelcatalog.Entry{
		Provider: modelcatalog.ProviderOpenRouter, ID: raw.ID, Name: modelcatalog.Known(raw.Name),
		Capabilities: capabilities,
		Protocol: modelcatalog.Protocol{
			API:                 modelcatalog.Known(modelcatalog.APIOpenAIChat),
			SupportsTemperature: modelcatalog.Known(slices.Contains(raw.Parameters, "temperature")),
			SupportsTopP:        modelcatalog.Known(slices.Contains(raw.Parameters, "top_p")),
			Compatibility: &modelcatalog.Compatibility{Chat: &modelcatalog.ChatCompatibility{
				MaxTokensField: modelcatalog.Known(maxTokens), InstructionRole: modelcatalog.Known(modelcatalog.InstructionSystem),
				ThinkingFormat: modelcatalog.Known(modelcatalog.ThinkingOpenRouter), ReasoningEffort: modelcatalog.Known(true),
				StreamingUsage: modelcatalog.Known(true), ReasoningContentReplay: modelcatalog.Known(true),
			}},
		},
		Pricing: pricing(raw.Pricing, revision),
	}
}

func positiveMinimum(values ...*int64) modelcatalog.Field[int64] {
	result := modelcatalog.Unknown[int64]()
	for _, value := range values {
		if value != nil && *value > 0 && (!result.Known || *value < result.Value) {
			result = modelcatalog.Known(*value)
		}
	}

	return result
}

func tokenRate(raw json.RawMessage) *float64 {
	var text string
	if json.Unmarshal(raw, &text) != nil {
		return nil
	}
	value, err := strconv.ParseFloat(text, 64)
	value *= 1_000_000
	if err != nil || value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return nil
	}

	return &value
}

func pricing(raw map[string]json.RawMessage, revision string) modelcatalog.Field[modelcatalog.Pricing] {
	quote := modelcatalog.Pricing{
		Source: OpenRouterSource, Version: revision,
		InputPerMillion: tokenRate(raw["prompt"]), OutputPerMillion: tokenRate(raw["completion"]),
		CacheReadPerMillion: tokenRate(raw["input_cache_read"]), CacheWritePerMillion: tokenRate(raw["input_cache_write"]),
	}
	// The host cannot price fixed/image/reasoning charges or scheduled rates.
	for _, key := range []string{"request", "image", "internal_reasoning"} {
		if value, exists := raw[key]; exists {
			if rate := tokenRate(value); rate == nil || *rate != 0 {
				return modelcatalog.Unknown[modelcatalog.Pricing]()
			}
		}
	}
	if encoded, ok := raw["overrides"]; ok {
		var overrides []map[string]json.RawMessage
		if json.Unmarshal(encoded, &overrides) != nil {
			return modelcatalog.Unknown[modelcatalog.Pricing]()
		}
		for _, override := range overrides {
			var threshold int64
			if json.Unmarshal(override["min_prompt_tokens"], &threshold) != nil || threshold <= 0 {
				return modelcatalog.Unknown[modelcatalog.Pricing]()
			}
			for key := range override {
				switch key {
				case "min_prompt_tokens", "prompt", "completion", "input_cache_read", "input_cache_write", "input_cache_write_1h", "audio", "input_audio_cache":
				default:
					return modelcatalog.Unknown[modelcatalog.Pricing]()
				}
			}
			// OpenRouter applies min_prompt_tokens strictly above this threshold.
			if quote.MaxInputTokens == nil || threshold < *quote.MaxInputTokens {
				quote.MaxInputTokens = new(threshold)
			}
		}
	}
	if quote.InputPerMillion == nil && quote.OutputPerMillion == nil && quote.CacheReadPerMillion == nil && quote.CacheWritePerMillion == nil {
		return modelcatalog.Unknown[modelcatalog.Pricing]()
	}

	return modelcatalog.Known(quote)
}
