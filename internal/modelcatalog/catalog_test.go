package modelcatalog

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func decodeTest(t *testing.T, source, value string) Layer {
	t.Helper()
	layer, err := DecodeYAML([]byte(value), source)
	if err != nil {
		t.Fatal(err)
	}

	return layer
}

func TestSparseLayersPreserveUnknownFalseAndEmptyLevels(t *testing.T) {
	base := decodeTest(t, "builtin", `version: 1
models:
  - provider: primary
    id: team.reasoner/v1
    capabilities:
      tools: true
      images: true
      reasoning: true
      reasoning_levels: [low, high, xhigh]
    protocol:
      api: openai_responses
      compatibility:
        responses:
          encrypted_reasoning: true
          strict_tools: true
`)
	user := decodeTest(t, "user", `version: 1
models:
  - provider: primary
    id: team.reasoner/v1
    capabilities:
      tools: false
      images: null
      reasoning_levels: []
    protocol:
      compatibility:
        responses:
          strict_tools: false
`)
	project := decodeTest(t, "project", `version: 1
models:
  - provider: primary
    id: team.reasoner/v1
    name: Local model
`)
	catalog, err := Merge(base, user, project)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := catalog.Lookup("primary", "team.reasoner/v1")
	if !ok {
		t.Fatal("literal model ID was lost")
	}
	if !entry.Capabilities.Tools.Known || entry.Capabilities.Tools.Value {
		t.Fatal("known false did not override true")
	}
	if !entry.Capabilities.Images.Present || entry.Capabilities.Images.Known {
		t.Fatal("explicit null did not mask inherited images")
	}
	if !entry.Capabilities.ReasoningLevels.Known || len(entry.Capabilities.ReasoningLevels.Value) != 0 {
		t.Fatal("explicit empty reasoning levels lost their known state")
	}
	if !entry.Capabilities.Reasoning.Value || !entry.Protocol.Compatibility.Responses.EncryptedReasoning.Value {
		t.Fatal("omitted values were not inherited")
	}
	if strict := entry.Protocol.Compatibility.Responses.StrictTools; !strict.Known || strict.Value {
		t.Fatal("protocol false did not survive the merge")
	}
	if entry.Origins["capabilities.images"] != "user" || entry.Origins["name"] != "project" {
		t.Fatalf("wrong provenance: %v", entry.Origins)
	}
	if _, ok := catalog.Lookup("other", "team.reasoner/v1"); ok {
		t.Fatal("metadata crossed provider identity")
	}

	data, err := json.Marshal(Layer{Version: Version, Models: catalog.Models()})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"reasoning_levels":[]`) || !strings.Contains(string(data), `"images":null`) {
		t.Fatalf("wire shape lost unknown/empty distinction: %s", data)
	}
	if _, err := DecodeJSON(data, "roundtrip"); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidLayersAreRejectedAtomically(t *testing.T) {
	cases := map[string]string{
		"version":                 `{"version":2,"models":[]}`,
		"missing version":         `{"models":[]}`,
		"fraction version":        `{"version":1.0,"models":[]}`,
		"case alias":              `{"version":1,"models":[{"provider":"p","id":"m","capabilities":{"Tools":true}}]}`,
		"null models":             `{"version":1,"models":null}`,
		"duplicate object key":    `{"version":1,"version":1,"models":[]}`,
		"unknown root":            `{"version":1,"models":[],"endpoint":"https://example.invalid"}`,
		"duplicate literal ID":    `{"version":1,"models":[{"provider":"p","id":"team.m/v1"},{"provider":"p","id":"team.m/v1"}]}`,
		"reserved endpoint":       `{"version":1,"models":[{"provider":"p","id":"m","endpoint":"https://example.invalid"}]}`,
		"headers":                 `{"version":1,"models":[{"provider":"p","id":"m","headers":{}}]}`,
		"null endpoint":           `{"version":1,"models":[{"provider":"p","id":"m","protocol":{"endpoint":null}}]}`,
		"wrong capability type":   `{"version":1,"models":[{"provider":"p","id":"m","capabilities":{"tools":"true"}}]}`,
		"null capabilities":       `{"version":1,"models":[{"provider":"p","id":"m","capabilities":null}]}`,
		"empty compatibility":     `{"version":1,"models":[{"provider":"p","id":"m","protocol":{"compatibility":{}}}]}`,
		"null block":              `{"version":1,"models":[{"provider":"p","id":"m","protocol":{"compatibility":{"chat":null}}}]}`,
		"multiple blocks":         `{"version":1,"models":[{"provider":"p","id":"m","protocol":{"compatibility":{"chat":{},"responses":{}}}}]}`,
		"mismatched block":        `{"version":1,"models":[{"provider":"p","id":"m","protocol":{"api":"anthropic_messages","compatibility":{"chat":{}}}}]}`,
		"unknown route":           `{"version":1,"models":[{"provider":"p","id":"m","protocol":{"api":"new_wire"}}]}`,
		"unknown compatibility":   `{"version":1,"models":[{"provider":"p","id":"m","protocol":{"compatibility":{"chat":{"new_flag":true}}}}]}`,
		"unknown enum":            `{"version":1,"models":[{"provider":"p","id":"m","protocol":{"compatibility":{"chat":{"thinking_format":"magic"}}}}]}`,
		"unsupported mode":        `{"version":1,"models":[{"provider":"p","id":"m","capabilities":{"reasoning_levels":["ultra"]}}]}`,
		"duplicate mode":          `{"version":1,"models":[{"provider":"p","id":"m","capabilities":{"reasoning_levels":["low","low"]}}]}`,
		"contradictory reasoning": `{"version":1,"models":[{"provider":"p","id":"m","capabilities":{"reasoning":false,"reasoning_levels":["low"]}}]}`,
		"reversed budget":         `{"version":1,"models":[{"provider":"p","id":"m","capabilities":{"reasoning_budget_min":2048,"reasoning_budget_max":1024}}]}`,
		"budget without modes":    `{"version":1,"models":[{"provider":"p","id":"m","capabilities":{"reasoning_levels":["low"],"reasoning_budget_min":1024}}]}`,
		"invalid numeric limit":   `{"version":1,"models":[{"provider":"p","id":"m","capabilities":{"context_window":0}}]}`,
		"fraction limit":          `{"version":1,"models":[{"provider":"p","id":"m","capabilities":{"context_window":1.5}}]}`,
		"unknown pricing key":     `{"version":1,"models":[{"provider":"p","id":"m","pricing":{"source":"x","version":"x","endpoint":"x"}}]}`,
		"negative price":          `{"version":1,"models":[{"provider":"p","id":"m","pricing":{"source":"x","version":"x","input_per_million":-1}}]}`,
		"multiple documents":      `{"version":1,"models":[]} {"version":1,"models":[]}`,
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			layer, err := DecodeJSON([]byte(value), "test")
			if err == nil || len(layer.Models) != 0 || layer.Version != 0 {
				t.Fatalf("invalid layer published: %+v, %v", layer, err)
			}
		})
	}
}

func TestMergeValidatesInheritedContradictions(t *testing.T) {
	base := decodeTest(t, "base", "version: 1\nmodels:\n  - provider: p\n    id: m\n    protocol:\n      api: openai_chat\n      compatibility:\n        chat: {}\n")
	bad := decodeTest(t, "override", "version: 1\nmodels:\n  - provider: p\n    id: m\n    protocol:\n      api: anthropic_messages\n")
	if _, err := Merge(base, bad); err == nil {
		t.Fatal("inherited incompatible block was accepted")
	}
	unknown := decodeTest(t, "override", "version: 1\nmodels:\n  - provider: p\n    id: m\n    protocol:\n      api: null\n")
	if _, err := Merge(base, unknown); err == nil {
		t.Fatal("unknown API with inherited compatibility was accepted")
	}
}

func TestCatalogCopiesProtectFrozenData(t *testing.T) {
	layer := decodeTest(t, "test", "version: 1\nmodels:\n  - provider: p\n    id: m\n    capabilities:\n      reasoning_levels: [low]\n    protocol:\n      api: openai_responses\n      compatibility:\n        responses:\n          strict_tools: true\n")
	catalog, err := Merge(layer)
	if err != nil {
		t.Fatal(err)
	}
	layer.Models[0].Capabilities.ReasoningLevels.Value[0] = "high"
	layer.Models[0].Protocol.Compatibility.Responses.StrictTools = Known(false)
	cloned, _ := catalog.Lookup("p", "m")
	cloned.Capabilities.ReasoningLevels.Value[0] = "max"
	cloned.Protocol.Compatibility.Responses.StrictTools = Known(false)
	cloned.Origins["capabilities.reasoning_levels"] = "changed"
	frozen, _ := catalog.Lookup("p", "m")
	if !reflect.DeepEqual(frozen.Capabilities.ReasoningLevels.Value, []string{"low"}) || !frozen.Protocol.Compatibility.Responses.StrictTools.Value || frozen.Origins["capabilities.reasoning_levels"] != "test" {
		t.Fatalf("frozen catalog mutated: %+v", frozen)
	}
}

func TestYAMLStrictShapesAndDocuments(t *testing.T) {
	for _, value := range []string{
		"version: 1\nversion: 1\nmodels: []\n",
		"version: 1\nmodels: []\n---\nversion: 1\nmodels: []\n",
		"version: 1.0\nmodels: []\n",
		"version: 1\nmodels:\n  - provider: p\n    id: m\n    capabilities:\n      context_window: 100.0\n",
	} {
		if _, err := DecodeYAML([]byte(value), "test"); err == nil {
			t.Errorf("accepted invalid YAML: %s", value)
		}
	}
}

func TestMapDecoderDoesNotNarrowFloatingPointVersion(t *testing.T) {
	if _, err := DecodeValue(map[string]any{"version": float64(1), "models": []any{}}, "cli"); err == nil {
		t.Fatal("float version was coerced to an integer")
	}
}

func TestProtocolSamplingFieldsUseSparseKnownSemantics(t *testing.T) {
	base, err := DecodeJSON([]byte(`{"version":1,"models":[{"provider":"p","id":"m","protocol":{"supports_temperature":false,"supports_top_p":true}}]}`), "builtin")
	if err != nil {
		t.Fatal(err)
	}
	user, err := DecodeYAML([]byte("version: 1\nmodels: [{provider: p, id: m, protocol: {supports_top_p: null}}]\n"), "global")
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := Merge(base, user)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := catalog.Lookup("p", "m")
	if !ok || !entry.Protocol.SupportsTemperature.Known || entry.Protocol.SupportsTemperature.Value || entry.Protocol.SupportsTopP.Known || !entry.Protocol.SupportsTopP.Present {
		t.Fatalf("protocol overlay lost false/null distinctions: %+v", entry.Protocol)
	}
	if entry.Origins["protocol.supports_temperature"] != "builtin" || entry.Origins["protocol.supports_top_p"] != "global" {
		t.Fatalf("protocol provenance: %v", entry.Origins)
	}
}
