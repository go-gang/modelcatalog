package modelcatalog

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestSamplingThinkingOverlayPreservesUnknownEmptyAndInheritance(t *testing.T) {
	base := decodeTest(t, "builtin", `version: 1
models:
  - provider: p
    id: m
    protocol:
      allowed_sampling_thinking: [off, low]
`)
	for _, fixture := range []struct {
		name, protocol string
		known          bool
		modes          []string
		source         string
	}{
		{"absent", "{}", true, []string{"off", "low"}, "builtin"},
		{"unknown", "{allowed_sampling_thinking: null}", false, nil, "project"},
		{"empty", "{allowed_sampling_thinking: []}", true, []string{}, "project"},
		{"replace", "{allowed_sampling_thinking: [provider_default, off]}", true, []string{"provider_default", "off"}, "project"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			overlay := decodeTest(t, "project", "version: 1\nmodels:\n  - provider: p\n    id: m\n    protocol: "+fixture.protocol+"\n")
			catalog, err := Merge(base, overlay)
			if err != nil {
				t.Fatal(err)
			}
			entry, ok := catalog.Lookup("p", "m")
			field := entry.Protocol.AllowedSamplingThinking
			if !ok || !field.Present || field.Known != fixture.known || !reflect.DeepEqual(field.Value, fixture.modes) {
				t.Fatalf("lost sampling restriction: %+v", field)
			}
			if entry.Origins["protocol.allowed_sampling_thinking"] != fixture.source {
				t.Fatalf("wrong sampling provenance: %v", entry.Origins)
			}
			if len(field.Value) > 0 {
				clone := entry.Clone()
				clone.Protocol.AllowedSamplingThinking.Value[0] = "mutated"
				if !reflect.DeepEqual(entry.Protocol.AllowedSamplingThinking.Value, fixture.modes) {
					t.Fatal("metadata clone aliases its source")
				}
				field.Value[0] = "caller mutation"
				again, _ := catalog.Lookup("p", "m")
				if !reflect.DeepEqual(again.Protocol.AllowedSamplingThinking.Value, fixture.modes) {
					t.Fatal("returned metadata aliases the catalog")
				}
			}
			body, err := json.Marshal(overlay)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeJSON(body, "roundtrip"); err != nil {
				t.Fatalf("sampling restriction cannot roundtrip: %s: %v", body, err)
			}
		})
	}
}

func TestSamplingThinkingRejectsUnknownDuplicateAndWrongTypes(t *testing.T) {
	for _, value := range []string{`["none"]`, `["off","off"]`, `false`, `"off"`, `[1]`} {
		if _, err := DecodeJSON([]byte(`{"version":1,"models":[{"provider":"p","id":"m","protocol":{"allowed_sampling_thinking":`+value+`}}]}`), "fixture"); err == nil {
			t.Fatalf("accepted invalid sampling modes: %s", value)
		}
	}
}
