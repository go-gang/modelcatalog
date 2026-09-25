// Package modelcatalog defines provider model metadata and validates sparse catalog layers.
package modelcatalog

import (
	"bytes"
	"encoding/json"
	"reflect"
)

// Version is independent of the enclosing model.yaml format version.
const Version = 1

// Field preserves absent, explicitly unknown, and known values in a sparse layer.
// A known false or empty list is authoritative; neither permits a legacy fallback.
type Field[T any] struct {
	Present bool
	Known   bool
	Value   T
}

// Known constructs an explicit value, including false and empty lists.
func Known[T any](value T) Field[T] { return Field[T]{Present: true, Known: true, Value: value} }

// Unknown masks an inherited value without asserting a replacement.
func Unknown[T any]() Field[T] { return Field[T]{Present: true} }

// IsZero allows the JSON writer to omit absent fields while preserving null.
func (f Field[T]) IsZero() bool { return !f.Present }

func (f Field[T]) present() bool { return f.Present }

func (f Field[T]) valueType() reflect.Type { return reflect.TypeFor[T]() }

// MarshalJSON preserves the public data format, never the Go representation.
func (f Field[T]) MarshalJSON() ([]byte, error) {
	if !f.Known {
		return []byte("null"), nil
	}
	if value := reflect.ValueOf(f.Value); value.Kind() == reflect.Slice && value.IsNil() {
		return []byte("[]"), nil
	}

	return json.Marshal(f.Value)
}

// UnmarshalJSON records presence before decoding a known value.
func (f *Field[T]) UnmarshalJSON(data []byte) error {
	*f = Unknown[T]()
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&f.Value); err != nil {
		return err
	}
	f.Known = true

	return nil
}

// Layer is both the embedded JSON envelope and the catalog section in YAML.
type Layer struct {
	Version int     `json:"version"`
	Models  []Entry `json:"models"`
	Source  string  `json:"-"`
}

// Entry is identified by the literal connection/provider and model ID pair.
// Origins records the layer that last supplied each field, including null.
type Entry struct {
	Provider     string            `json:"provider"`
	ID           string            `json:"id"`
	Name         Field[string]     `json:"name,omitzero"`
	Capabilities Capabilities      `json:"capabilities,omitzero"`
	Protocol     Protocol          `json:"protocol,omitzero"`
	Pricing      Field[Pricing]    `json:"pricing,omitzero"`
	Origins      map[string]string `json:"-"`
}

// Clone detaches mutable metadata so a captured execution cannot be changed by
// its caller or by a later catalog reload.
func (entry Entry) Clone() Entry { return cloneEntry(entry) }

// Pricing is an atomic quote: replacing it also supplies fresh provenance.
// Nil rates remain unknown. Rates describe standard processing in USD per
// million tokens, not subscription billing or an invoice from the provider.
type Pricing struct {
	Source               string   `json:"source"`
	Version              string   `json:"version"`
	InputPerMillion      *float64 `json:"input_per_million,omitempty"`
	OutputPerMillion     *float64 `json:"output_per_million,omitempty"`
	CacheReadPerMillion  *float64 `json:"cache_read_per_million,omitempty"`
	CacheWritePerMillion *float64 `json:"cache_write_per_million,omitempty"`
	// Above this inclusive-input threshold the quote is unknown, not extrapolated.
	MaxInputTokens *int64 `json:"max_input_tokens,omitempty"`
}

// Capabilities describes only verified optional features and token limits.
type Capabilities struct {
	Tools              Field[bool]     `json:"tools,omitzero"`
	Images             Field[bool]     `json:"images,omitzero"`
	System             Field[bool]     `json:"system,omitzero"`
	Streaming          Field[bool]     `json:"streaming,omitzero"`
	Reasoning          Field[bool]     `json:"reasoning,omitzero"`
	ReasoningLevels    Field[[]string] `json:"reasoning_levels,omitzero"`
	ContextWindow      Field[int64]    `json:"context_window,omitzero"`
	MaxOutputTokens    Field[int64]    `json:"max_output_tokens,omitzero"`
	ReasoningBudgetMin Field[int64]    `json:"reasoning_budget_min,omitzero"`
	ReasoningBudgetMax Field[int64]    `json:"reasoning_budget_max,omitzero"`
}

// API selects a known serializer; it never selects an endpoint or credentials.
type API string

// Version 1 serializer identifiers.
const (
	APIOpenAIChat           API = "openai_chat"
	APIOpenAIResponses      API = "openai_responses"
	APIOpenAICodexResponses API = "openai_codex_responses"
	APIAnthropicMessages    API = "anthropic_messages"
)

// Protocol is optional for extension-provided handlers.
type Protocol struct {
	API                     Field[API]      `json:"api,omitzero"`
	SupportsTemperature     Field[bool]     `json:"supports_temperature,omitzero"`
	SupportsTopP            Field[bool]     `json:"supports_top_p,omitzero"`
	AllowedSamplingThinking Field[[]string] `json:"allowed_sampling_thinking,omitzero"`
	Compatibility           *Compatibility  `json:"compatibility,omitempty"`
}

// Compatibility contains exactly one block when present. Its fields merge
// sparsely only with another block of the same protocol.
type Compatibility struct {
	Chat      *ChatCompatibility      `json:"chat,omitempty"`
	Responses *ResponsesCompatibility `json:"responses,omitempty"`
	Anthropic *AnthropicCompatibility `json:"anthropic,omitempty"`
}

// MaxTokensField selects the OpenAI Chat output-limit wire field.
type MaxTokensField string

// Version 1 output-limit field spellings.
const (
	MaxTokensCompletion MaxTokensField = "completion"
	MaxTokensLegacy     MaxTokensField = "legacy"
)

// InstructionRole selects the OpenAI Chat instruction message role.
type InstructionRole string

// Supported instruction message roles.
const (
	InstructionSystem    InstructionRole = "system"
	InstructionDeveloper InstructionRole = "developer"
)

// ThinkingFormat selects a known compatible reasoning serializer.
type ThinkingFormat string

// Supported Chat reasoning formats, independent of SDK enums.
const (
	ThinkingOpenAI     ThinkingFormat = "openai"
	ThinkingOpenRouter ThinkingFormat = "openrouter"
	ThinkingDeepSeek   ThinkingFormat = "deepseek"
	ThinkingTogether   ThinkingFormat = "together"
	ThinkingZAI        ThinkingFormat = "zai"
	ThinkingQwen       ThinkingFormat = "qwen"
	ThinkingString     ThinkingFormat = "string_thinking"
)

// ChatCompatibility describes serializer switches for OpenAI-compatible Chat.
type ChatCompatibility struct {
	MaxTokensField           Field[MaxTokensField]  `json:"max_tokens_field,omitzero"`
	InstructionRole          Field[InstructionRole] `json:"instruction_role,omitzero"`
	ThinkingFormat           Field[ThinkingFormat]  `json:"thinking_format,omitzero"`
	ReasoningEffort          Field[bool]            `json:"reasoning_effort,omitzero"`
	StreamingUsage           Field[bool]            `json:"streaming_usage,omitzero"`
	FinishReason             Field[bool]            `json:"finish_reason,omitzero"`
	ToolResultName           Field[bool]            `json:"tool_result_name,omitzero"`
	ToolResultImageFallback  Field[bool]            `json:"tool_result_image_fallback,omitzero"`
	AssistantAfterToolResult Field[bool]            `json:"assistant_after_tool_result,omitzero"`
	ReasoningContentReplay   Field[bool]            `json:"reasoning_content_replay,omitzero"`
	StrictTools              Field[bool]            `json:"strict_tools,omitzero"`
}

// ResponsesCompatibility describes serializer switches shared by Responses routes.
type ResponsesCompatibility struct {
	DeveloperRole      Field[bool] `json:"developer_role,omitzero"`
	EncryptedReasoning Field[bool] `json:"encrypted_reasoning,omitzero"`
	StrictTools        Field[bool] `json:"strict_tools,omitzero"`
}

// AnthropicCompatibility describes serializer switches for Anthropic Messages.
type AnthropicCompatibility struct {
	AdaptiveThinking        Field[bool] `json:"adaptive_thinking,omitzero"`
	Temperature             Field[bool] `json:"temperature,omitzero"`
	EmptyThinkingSignature  Field[bool] `json:"empty_thinking_signature,omitzero"`
	EagerToolInputStreaming Field[bool] `json:"eager_tool_input_streaming,omitzero"`
	StrictTools             Field[bool] `json:"strict_tools,omitzero"`
}
