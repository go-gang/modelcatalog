package cataloggen

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"

	"github.com/go-gang/modelcatalog/internal/modelcatalog"
)

func fixtureModel(t *testing.T, id string) sourceModel {
	t.Helper()
	var raw sourceModel
	if err := json.Unmarshal([]byte(`{
		"name":"Fixture","architecture":{"input_modalities":["text"],"output_modalities":["text"],"tokenizer":"Other"},
		"context_length":200000,"top_provider":{"context_length":128000,"max_completion_tokens":8192},
		"supported_parameters":["tools","tool_choice","max_tokens","reasoning"],
		"pricing":{"prompt":"0","completion":"0.000002"}
	}`), &raw); err != nil {
		t.Fatal(err)
	}
	raw.ID = id

	return raw
}

func sourceJSON(t *testing.T, models ...sourceModel) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{"data": models})
	if err != nil {
		t.Fatal(err)
	}

	return data
}

func TestProjectionPreservesLiteralMetadataWithoutThinkingInference(t *testing.T) {
	raw := fixtureModel(t, "team/claude-99-gpt-reasoner:free")
	output, report, err := Build(sourceJSON(t, raw), modelcatalog.Layer{Version: 1}, "2026-09-25", "fixture-v1")
	if err != nil {
		t.Fatal(err)
	}
	layer, err := modelcatalog.DecodeJSON(output, "generated")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(report.Included, []string{raw.ID}) || len(layer.Models) != 1 {
		t.Fatalf("wrong literal selection: %+v", report)
	}
	entry := layer.Models[0]
	caps := entry.Capabilities
	if !caps.Tools.Value || !caps.System.Value || !caps.Streaming.Value || !caps.Images.Known || caps.Images.Value || !caps.Reasoning.Value {
		t.Fatalf("incorrect projected capabilities: %+v", caps)
	}
	if !caps.ReasoningLevels.Present || caps.ReasoningLevels.Known || caps.ReasoningBudgetMin.Known || caps.ReasoningBudgetMax.Known {
		t.Fatalf("invented exact thinking support: %+v", caps)
	}
	if caps.ContextWindow.Value != 128000 || caps.MaxOutputTokens.Value != 8192 {
		t.Fatalf("provider limits lost: %+v", caps)
	}
	if !entry.Protocol.SupportsTemperature.Known || entry.Protocol.SupportsTemperature.Value || !entry.Protocol.SupportsTopP.Known || entry.Protocol.SupportsTopP.Value {
		t.Fatal("missing supported parameters must not enable legacy sampling defaults")
	}
	quote := entry.Pricing.Value
	if !entry.Pricing.Known || quote.InputPerMillion == nil || *quote.InputPerMillion != 0 || *quote.OutputPerMillion != 2 || quote.CacheReadPerMillion != nil || quote.CacheWritePerMillion != nil || quote.Source != OpenRouterSource || quote.Version != "fixture-v1" {
		t.Fatalf("zero/unknown price or provenance lost: %+v", quote)
	}
	raw.Parameters = []string{"tools", "tool_choice", "max_completion_tokens", "temperature"}
	raw.ContextLength, raw.TopProvider.ContextLength, raw.TopProvider.MaxOutput = nil, nil, nil
	entry = project(raw, "fixture-v1")
	if entry.Protocol.Compatibility.Chat.MaxTokensField.Value != modelcatalog.MaxTokensCompletion || !entry.Protocol.SupportsTemperature.Value || entry.Capabilities.ContextWindow.Known || entry.Capabilities.MaxOutputTokens.Known {
		t.Fatal("completion parameter or unknown limits were inferred incorrectly")
	}
}

func TestProjectionExcludesIncompatibleRoutesAndKeepsCatalogVariants(t *testing.T) {
	base := fixtureModel(t, "team/base")
	cases := []struct {
		name   string
		change func(*sourceModel)
		reason string
	}{
		{"image output", func(m *sourceModel) { m.Architecture.Output = []string{"image", "text"} }, "unsupported_modalities"},
		{"audio input", func(m *sourceModel) { m.Architecture.Input = []string{"audio"} }, "unsupported_modalities"},
		{"tools absent", func(m *sourceModel) { m.Parameters = []string{"tool_choice", "max_tokens"} }, "tools_and_tool_choice_required"},
		{"tool choice absent", func(m *sourceModel) { m.Parameters = []string{"tools", "max_tokens"} }, "tools_and_tool_choice_required"},
		{"batch", func(m *sourceModel) { m.ID += ":batch" }, "batch_api"},
		{"router metadata", func(m *sourceModel) { m.Architecture.Tokenizer = "Router" }, "dynamic_router"},
		{"unknown limit support", func(m *sourceModel) { m.Parameters = []string{"tools", "tool_choice"} }, "output_limit_parameter_unknown"},
		{"expired", func(m *sourceModel) { m.Expiration = new("2026-09-25") }, "expired"},
		{"free variant", func(m *sourceModel) { m.ID += ":free" }, ""},
		{"name is not metadata", func(m *sourceModel) { m.ID = "router/batch-thinking" }, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := base
			tc.change(&raw)
			_, report, err := Build(sourceJSON(t, raw), modelcatalog.Layer{Version: 1}, "2026-09-25", "v1")
			if err != nil {
				t.Fatal(err)
			}
			if tc.reason == "" {
				if !slices.Equal(report.Included, []string{raw.ID}) {
					t.Fatal("compatible literal ID was excluded")
				}
			} else if len(report.Included) != 0 || !slices.Equal(report.Excluded[tc.reason], []string{raw.ID}) {
				t.Fatalf("wrong exclusion: %+v", report)
			}
		})
	}
}

func TestProjectionRejectsPartialDuplicateAndMissingReviewedInputs(t *testing.T) {
	raw := fixtureModel(t, "team/base")
	core := modelcatalog.Layer{Version: 1}
	if _, _, err := Build(sourceJSON(t, raw, raw), core, "2026-09-25", "v1"); err == nil {
		t.Fatal("duplicate literal ID accepted")
	}
	for _, tail := range []string{`,"total_count":2}`, `,"links":{"next":"/models?page=2"}}`} {
		input := append(bytes.TrimSuffix(sourceJSON(t, raw), []byte("}")), []byte(tail)...)
		if _, _, err := Build(input, core, "2026-09-25", "v1"); err == nil {
			t.Fatal("partial source snapshot accepted")
		}
	}
	core.Models = []modelcatalog.Entry{{Provider: "openrouter", ID: "missing-reviewed-model"}}
	if _, _, err := Build(sourceJSON(t, raw), core, "2026-09-25", "v1"); err == nil {
		t.Fatal("missing reviewed entry was silently retained or dropped")
	}
}

func TestConditionalPricesRemainUnknownOutsideRepresentableQuote(t *testing.T) {
	cases := []struct {
		name  string
		json  string
		known bool
		limit int64
	}{
		{"long context", `{"prompt":"0.000001","completion":"0.000005","overrides":[{"min_prompt_tokens":200000,"prompt":"0.000002"},{"min_prompt_tokens":100000,"completion":"0.000009"}]}`, true, 100000},
		{"scheduled", `{"prompt":"0.000001","overrides":[{"utc_start":0,"utc_end":1200,"prompt":"0.000002"}]}`, false, 0},
		{"unknown condition", `{"prompt":"0.000001","overrides":[{"min_prompt_tokens":100000,"new_condition":true}]}`, false, 0},
		{"image charge", `{"prompt":"0.000001","image":"0.001"}`, false, 0},
		{"reasoning charge", `{"completion":"0.000001","internal_reasoning":"0.000002"}`, false, 0},
		{"negative sentinel", `{"prompt":"-1","completion":"-1"}`, false, 0},
		{"missing rates", `{}`, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var raw map[string]json.RawMessage
			if err := json.Unmarshal([]byte(tc.json), &raw); err != nil {
				t.Fatal(err)
			}
			quote := pricing(raw, "v1")
			if quote.Known != tc.known || !quote.Present {
				t.Fatalf("incorrect known state: %+v", quote)
			}
			if tc.limit != 0 && (quote.Value.MaxInputTokens == nil || *quote.Value.MaxInputTokens != tc.limit) {
				t.Fatalf("wrong inclusive quote limit: %+v", quote)
			}
		})
	}
}
