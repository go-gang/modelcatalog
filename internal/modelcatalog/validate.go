package modelcatalog

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode"
)

// Validate rejects an entire invalid layer. Sparse entries may inherit their
// route; Merge additionally validates the complete effective entries.
func (layer Layer) Validate() error {
	if layer.Version != Version {
		return fmt.Errorf("catalog %s: unsupported version %d", layer.Source, layer.Version)
	}
	seen := map[key]struct{}{}
	for _, entry := range layer.Models {
		identity := key{entry.Provider, entry.ID}
		if _, duplicate := seen[identity]; duplicate {
			return fmt.Errorf("catalog %s: duplicate model %q/%q", layer.Source, entry.Provider, entry.ID)
		}
		seen[identity] = struct{}{}
		if err := validateEntry(entry, false); err != nil {
			return fmt.Errorf("catalog %s model %q/%q: %w", layer.Source, entry.Provider, entry.ID, err)
		}
	}

	return nil
}

func validateEntry(entry Entry, effective bool) error {
	if !validIdentity(entry.Provider) || !validIdentity(entry.ID) {
		return errors.New("provider and id must be nonempty literal strings without control characters or surrounding whitespace")
	}
	if entry.Name.Known && strings.TrimSpace(entry.Name.Value) == "" {
		return errors.New("name must not be empty")
	}
	if err := validateCapabilities(entry.Capabilities); err != nil {
		return err
	}
	if entry.Pricing.Known {
		if err := validatePricing(entry.Pricing.Value); err != nil {
			return err
		}
		if entry.Protocol.API.Known && entry.Protocol.API.Value == APIOpenAICodexResponses {
			return errors.New("subscription pricing must remain unknown")
		}
	}

	return validateProtocol(entry.Protocol, effective)
}

func validatePricing(pricing Pricing) error {
	if strings.TrimSpace(pricing.Source) == "" || strings.TrimSpace(pricing.Version) == "" {
		return errors.New("pricing requires source and version")
	}
	for _, rate := range []*float64{pricing.InputPerMillion, pricing.OutputPerMillion, pricing.CacheReadPerMillion, pricing.CacheWritePerMillion} {
		if rate != nil && (*rate < 0 || math.IsNaN(*rate) || math.IsInf(*rate, 0)) {
			return errors.New("pricing rates must be finite nonnegative numbers or null")
		}
	}
	if pricing.MaxInputTokens != nil && *pricing.MaxInputTokens <= 0 {
		return errors.New("pricing.max_input_tokens must be positive or null")
	}

	return nil
}

func validIdentity(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && !strings.ContainsFunc(value, unicode.IsControl)
}

func validateCapabilities(caps Capabilities) error {
	for _, item := range []struct {
		name  string
		field Field[int64]
	}{
		{"context_window", caps.ContextWindow},
		{"max_output_tokens", caps.MaxOutputTokens},
		{"reasoning_budget_min", caps.ReasoningBudgetMin},
		{"reasoning_budget_max", caps.ReasoningBudgetMax},
	} {
		if item.field.Known && item.field.Value <= 0 {
			return fmt.Errorf("capabilities.%s must be positive or null", item.name)
		}
	}
	if caps.ContextWindow.Known && caps.MaxOutputTokens.Known && caps.MaxOutputTokens.Value > caps.ContextWindow.Value {
		return errors.New("max_output_tokens exceeds context_window")
	}
	if caps.ReasoningBudgetMin.Known && caps.ReasoningBudgetMax.Known && caps.ReasoningBudgetMin.Value > caps.ReasoningBudgetMax.Value {
		return errors.New("reasoning_budget_min exceeds reasoning_budget_max")
	}
	if caps.ReasoningBudgetMax.Known && caps.MaxOutputTokens.Known && caps.ReasoningBudgetMax.Value >= caps.MaxOutputTokens.Value {
		return errors.New("reasoning_budget_max must be below max_output_tokens")
	}
	if caps.ReasoningBudgetMin.Known && caps.MaxOutputTokens.Known && caps.ReasoningBudgetMin.Value >= caps.MaxOutputTokens.Value {
		return errors.New("reasoning_budget_min must be below max_output_tokens")
	}
	if caps.ReasoningLevels.Known {
		seen := map[string]bool{}
		for _, level := range caps.ReasoningLevels.Value {
			if !slices.Contains([]string{"off", "minimal", "low", "medium", "high", "xhigh", "max", "budget"}, level) {
				return fmt.Errorf("unknown reasoning level %q", level)
			}
			if seen[level] {
				return fmt.Errorf("duplicate reasoning level %q", level)
			}
			seen[level] = true
		}
		if len(seen) > 0 && caps.Reasoning.Known && !caps.Reasoning.Value {
			return errors.New("reasoning_levels contradict reasoning:false")
		}
		if len(seen) > 0 && !seen["budget"] && (caps.ReasoningBudgetMin.Known || caps.ReasoningBudgetMax.Known) {
			return errors.New("reasoning budget bounds require budget in reasoning_levels")
		}
	}
	if caps.Reasoning.Known && !caps.Reasoning.Value && (caps.ReasoningBudgetMin.Known || caps.ReasoningBudgetMax.Known) {
		return errors.New("reasoning budget bounds contradict reasoning:false")
	}

	return nil
}

func validateProtocol(protocol Protocol, effective bool) error {
	if protocol.AllowedSamplingThinking.Known {
		seen := map[string]bool{}
		for _, mode := range protocol.AllowedSamplingThinking.Value {
			if !slices.Contains([]string{"provider_default", "off", "minimal", "low", "medium", "high", "xhigh", "max", "budget"}, mode) {
				return fmt.Errorf("unknown protocol.allowed_sampling_thinking mode %q", mode)
			}
			if seen[mode] {
				return fmt.Errorf("duplicate protocol.allowed_sampling_thinking mode %q", mode)
			}
			seen[mode] = true
		}
	}
	if protocol.API.Known {
		switch protocol.API.Value {
		case APIOpenAIChat, APIOpenAIResponses, APIOpenAICodexResponses, APIAnthropicMessages:
		default:
			return fmt.Errorf("unknown protocol.api %q", protocol.API.Value)
		}
	}
	compat := protocol.Compatibility
	if compat == nil {
		return nil
	}
	count := 0
	if compat.Chat != nil {
		count++
	}
	if compat.Responses != nil {
		count++
	}
	if compat.Anthropic != nil {
		count++
	}
	if count != 1 {
		return errors.New("protocol.compatibility must contain exactly one block")
	}
	if effective && !protocol.API.Known {
		return errors.New("protocol.compatibility requires a known protocol.api")
	}
	if protocol.API.Known {
		matches := (protocol.API.Value == APIOpenAIChat && compat.Chat != nil) ||
			((protocol.API.Value == APIOpenAIResponses || protocol.API.Value == APIOpenAICodexResponses) && compat.Responses != nil) ||
			(protocol.API.Value == APIAnthropicMessages && compat.Anthropic != nil)
		if !matches {
			return errors.New("protocol.compatibility block does not match protocol.api")
		}
	}
	if chat := compat.Chat; chat != nil {
		if err := validateEnum("max_tokens_field", chat.MaxTokensField, MaxTokensCompletion, MaxTokensLegacy); err != nil {
			return err
		}
		if err := validateEnum("instruction_role", chat.InstructionRole, InstructionSystem, InstructionDeveloper); err != nil {
			return err
		}
		if err := validateEnum("thinking_format", chat.ThinkingFormat, ThinkingOpenAI, ThinkingOpenRouter, ThinkingDeepSeek, ThinkingTogether, ThinkingZAI, ThinkingQwen, ThinkingString); err != nil {
			return err
		}
	}

	return nil
}

func validateEnum[T ~string](name string, field Field[T], allowed ...T) error {
	if field.Known && !slices.Contains(allowed, field.Value) {
		return fmt.Errorf("unknown protocol.compatibility.chat.%s %q", name, field.Value)
	}

	return nil
}
