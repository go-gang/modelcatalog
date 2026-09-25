package modelcatalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
)

type key struct {
	provider string
	id       string
}

// Catalog is immutable after construction. Accessors return detached copies.
type Catalog struct {
	entries  map[key]Entry
	ordered  []key
	revision string
}

// Merge applies layers from low to high precedence and publishes no partial
// result on error. The host keeps its previous Catalog when this returns an error.
func Merge(layers ...Layer) (Catalog, error) {
	result := Catalog{entries: map[key]Entry{}}
	for _, layer := range layers {
		if err := layer.Validate(); err != nil {
			return Catalog{}, err
		}
		for _, entry := range layer.Models {
			identity := key{entry.Provider, entry.ID}
			effective, found := result.entries[identity]
			if !found {
				effective = Entry{Provider: entry.Provider, ID: entry.ID, Origins: map[string]string{}}
				result.ordered = append(result.ordered, identity)
			}
			mergeEntry(&effective, entry, layer.Source)
			result.entries[identity] = effective
		}
	}
	slices.SortFunc(result.ordered, func(a, b key) int {
		if c := strings.Compare(a.provider, b.provider); c != 0 {
			return c
		}

		return strings.Compare(a.id, b.id)
	})
	for _, identity := range result.ordered {
		entry := result.entries[identity]
		if err := validateEntry(entry, true); err != nil {
			return Catalog{}, fmt.Errorf("effective catalog model %q/%q: %w", entry.Provider, entry.ID, err)
		}
		result.entries[identity] = cloneEntry(entry)
	}
	data, err := json.Marshal(Layer{Version: Version, Models: result.Models()})
	if err != nil {
		return Catalog{}, fmt.Errorf("serialize effective catalog: %w", err)
	}
	digest := sha256.Sum256(data)
	result.revision = hex.EncodeToString(digest[:])

	return result, nil
}

// Lookup performs exact provider and ID matching; no prefix or case folding.
func (catalog Catalog) Lookup(provider, id string) (Entry, bool) {
	entry, found := catalog.entries[key{provider, id}]

	return cloneEntry(entry), found
}

// Models returns sorted, detached entries for an offline selector.
func (catalog Catalog) Models() []Entry {
	entries := make([]Entry, 0, len(catalog.ordered))
	for _, identity := range catalog.ordered {
		entries = append(entries, cloneEntry(catalog.entries[identity]))
	}

	return entries
}

// Revision fingerprints effective metadata, independent of local source paths.
// The host uses its own publication generation for concurrent model state.
func (catalog Catalog) Revision() string { return catalog.revision }

func mergeEntry(dst *Entry, src Entry, source string) {
	if src.Name.Present {
		dst.Name = src.Name
		dst.Origins["name"] = source
	}
	if src.Pricing.Present {
		dst.Pricing = src.Pricing
		dst.Origins["pricing"] = source
	}
	mergeFields(&dst.Capabilities, src.Capabilities, "capabilities", dst.Origins, source)
	if src.Protocol.API.Present {
		dst.Protocol.API = src.Protocol.API
		dst.Origins["protocol.api"] = source
	}
	if src.Protocol.SupportsTemperature.Present {
		dst.Protocol.SupportsTemperature = src.Protocol.SupportsTemperature
		dst.Origins["protocol.supports_temperature"] = source
	}
	if src.Protocol.SupportsTopP.Present {
		dst.Protocol.SupportsTopP = src.Protocol.SupportsTopP
		dst.Origins["protocol.supports_top_p"] = source
	}
	if src.Protocol.AllowedSamplingThinking.Present {
		dst.Protocol.AllowedSamplingThinking = src.Protocol.AllowedSamplingThinking
		dst.Origins["protocol.allowed_sampling_thinking"] = source
	}
	compat := src.Protocol.Compatibility
	if compat == nil {
		return
	}
	if dst.Protocol.Compatibility == nil || !sameBlock(dst.Protocol.Compatibility, compat) {
		dst.Protocol.Compatibility = &Compatibility{}
		for path := range dst.Origins {
			if strings.HasPrefix(path, "protocol.compatibility.") {
				delete(dst.Origins, path)
			}
		}
	}
	dst.Origins["protocol.compatibility"] = source
	target := dst.Protocol.Compatibility
	if compat.Chat != nil {
		if target.Chat == nil {
			target.Chat = &ChatCompatibility{}
		}
		mergeFields(target.Chat, *compat.Chat, "protocol.compatibility.chat", dst.Origins, source)
	}
	if compat.Responses != nil {
		if target.Responses == nil {
			target.Responses = &ResponsesCompatibility{}
		}
		mergeFields(target.Responses, *compat.Responses, "protocol.compatibility.responses", dst.Origins, source)
	}
	if compat.Anthropic != nil {
		if target.Anthropic == nil {
			target.Anthropic = &AnthropicCompatibility{}
		}
		mergeFields(target.Anthropic, *compat.Anthropic, "protocol.compatibility.anthropic", dst.Origins, source)
	}
}

// These schema-owned structs contain only Field values. Reflection centralizes
// sparse presence handling so an added typed field cannot silently fail to merge.
func mergeFields[T any](target *T, source T, prefix string, origins map[string]string, layer string) {
	dst, src := reflect.ValueOf(target).Elem(), reflect.ValueOf(source)
	for i := 0; i < src.NumField(); i++ {
		field := src.Field(i).Interface().(interface{ present() bool })
		if field.present() {
			dst.Field(i).Set(src.Field(i))
			name, _, _ := strings.Cut(src.Type().Field(i).Tag.Get("json"), ",")
			origins[prefix+"."+name] = layer
		}
	}
}

func sameBlock(a, b *Compatibility) bool {
	return (a.Chat != nil && b.Chat != nil) || (a.Responses != nil && b.Responses != nil) || (a.Anthropic != nil && b.Anthropic != nil)
}

func cloneEntry(entry Entry) Entry {
	entry.Origins = maps.Clone(entry.Origins)
	entry.Capabilities.ReasoningLevels.Value = slices.Clone(entry.Capabilities.ReasoningLevels.Value)
	entry.Protocol.AllowedSamplingThinking.Value = slices.Clone(entry.Protocol.AllowedSamplingThinking.Value)
	entry.Pricing.Value.InputPerMillion = clonePointer(entry.Pricing.Value.InputPerMillion)
	entry.Pricing.Value.OutputPerMillion = clonePointer(entry.Pricing.Value.OutputPerMillion)
	entry.Pricing.Value.CacheReadPerMillion = clonePointer(entry.Pricing.Value.CacheReadPerMillion)
	entry.Pricing.Value.CacheWritePerMillion = clonePointer(entry.Pricing.Value.CacheWritePerMillion)
	entry.Pricing.Value.MaxInputTokens = clonePointer(entry.Pricing.Value.MaxInputTokens)
	if entry.Protocol.Compatibility != nil {
		compat := *entry.Protocol.Compatibility
		if compat.Chat != nil {
			value := *compat.Chat
			compat.Chat = &value
		}
		if compat.Responses != nil {
			value := *compat.Responses
			compat.Responses = &value
		}
		if compat.Anthropic != nil {
			value := *compat.Anthropic
			compat.Anthropic = &value
		}
		entry.Protocol.Compatibility = &compat
	}

	return entry
}

func clonePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	cloned := *value

	return &cloned
}
