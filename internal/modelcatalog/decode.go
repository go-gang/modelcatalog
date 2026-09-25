package modelcatalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// DecodeJSON validates one complete sparse layer before returning any entries.
// In particular duplicate object keys cannot silently overwrite earlier data.
func DecodeJSON(data []byte, source string) (Layer, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := readJSONValue(decoder)
	if err != nil {
		return Layer{}, fmt.Errorf("catalog %s: %w", source, err)
	}
	if _, err = decoder.Token(); !errors.Is(err, io.EOF) {
		return Layer{}, fmt.Errorf("catalog %s: expected one JSON document", source)
	}

	return decodeValue(value, source)
}

// DecodeYAML decodes the catalog object, not the enclosing model.yaml file.
// The host extracts this section before dotted-path/mapstructure processing.
func DecodeYAML(data []byte, source string) (Layer, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return Layer{}, fmt.Errorf("catalog %s: %w", source, err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Layer{}, fmt.Errorf("catalog %s: expected one YAML document", source)
	}
	if len(document.Content) != 1 {
		return Layer{}, fmt.Errorf("catalog %s: expected an object", source)
	}
	value, err := readYAMLValue(document.Content[0])
	if err != nil {
		return Layer{}, fmt.Errorf("catalog %s: %w", source, err)
	}

	return decodeValue(value, source)
}

// DecodeValue accepts the string-keyed object obtained by a host YAML loader.
// Prefer DecodeYAML when duplicate-key checks have not already been performed.
func DecodeValue(value any, source string) (Layer, error) {
	data, err := json.Marshal(preserveFloatNumbers(value))
	if err != nil {
		return Layer{}, fmt.Errorf("catalog %s: invalid data: %w", source, err)
	}

	return DecodeJSON(data, source)
}

// A loader-supplied float64(1) is still a float, not the integer 1. Encoding it
// in exponent form prevents JSON marshaling from silently narrowing its type.
func preserveFloatNumbers(value any) any {
	switch typed := value.(type) {
	case float64:
		return json.Number(strconv.FormatFloat(typed, 'e', -1, 64))
	case float32:
		return json.Number(strconv.FormatFloat(float64(typed), 'e', -1, 32))
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = preserveFloatNumbers(item)
		}

		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = preserveFloatNumbers(item)
		}

		return out
	}

	return value
}

func decodeValue(value any, source string) (Layer, error) {
	if err := checkContainers(value); err != nil {
		return Layer{}, fmt.Errorf("catalog %s: %w", source, err)
	}
	if err := checkExactKeys(value, reflect.TypeFor[Layer](), "catalog"); err != nil {
		return Layer{}, fmt.Errorf("catalog %s: %w", source, err)
	}
	data, err := json.Marshal(value)
	if err != nil {
		return Layer{}, fmt.Errorf("catalog %s: %w", source, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var layer Layer
	if err := decoder.Decode(&layer); err != nil {
		return Layer{}, fmt.Errorf("catalog %s: %w", source, err)
	}
	layer.Source = source
	if err := layer.Validate(); err != nil {
		return Layer{}, err
	}

	return layer, nil
}

// encoding/json deliberately matches struct keys case-insensitively. A catalog
// schema does not: only the declared spellings are accepted at every depth.
func checkExactKeys(value any, typ reflect.Type, path string) error {
	if value == nil {
		return nil
	}
	if typ.Kind() == reflect.Pointer {
		return checkExactKeys(value, typ.Elem(), path)
	}
	if field, ok := reflect.TypeAssert[interface{ valueType() reflect.Type }](reflect.Zero(typ)); ok {
		return checkExactKeys(value, field.valueType(), path)
	}
	if typ.Kind() == reflect.Slice {
		if values, ok := value.([]any); ok {
			for i, item := range values {
				if err := checkExactKeys(item, typ.Elem(), fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
		}

		return nil
	}
	if typ.Kind() != reflect.Struct {
		return nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	fields := make(map[string]reflect.Type, typ.NumField())
	for field := range typ.Fields() {
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name != "" && name != "-" {
			fields[name] = field.Type
		}
	}
	for name, item := range object {
		fieldType, found := fields[name]
		if !found {
			return fmt.Errorf("%s: unknown key %q", path, name)
		}
		if err := checkExactKeys(item, fieldType, path+"."+name); err != nil {
			return err
		}
	}

	return nil
}

func checkContainers(value any) error {
	root, ok := value.(map[string]any)
	if !ok {
		return errors.New("expected an object")
	}
	if _, ok := root["version"]; !ok {
		return errors.New("version is required")
	}
	models, ok := root["models"].([]any)
	if !ok {
		return errors.New("models must be a list")
	}
	for i, value := range models {
		entry, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("models[%d] must be an object", i)
		}
		if err := checkObjects(entry, "capabilities", "protocol"); err != nil {
			return fmt.Errorf("models[%d]: %w", i, err)
		}
		if protocol, ok := entry["protocol"].(map[string]any); ok {
			if err := checkObjects(protocol, "compatibility"); err != nil {
				return fmt.Errorf("models[%d].protocol: %w", i, err)
			}
			if compat, ok := protocol["compatibility"].(map[string]any); ok {
				if len(compat) != 1 {
					return fmt.Errorf("models[%d].protocol.compatibility must contain exactly one block", i)
				}
				if err := checkObjects(compat, "chat", "responses", "anthropic"); err != nil {
					return fmt.Errorf("models[%d].protocol.compatibility: %w", i, err)
				}
			}
		}
	}

	return nil
}

func checkObjects(object map[string]any, keys ...string) error {
	for _, key := range keys {
		if value, found := object[key]; found {
			if _, ok := value.(map[string]any); !ok {
				return fmt.Errorf("%s must be an object", key)
			}
		}
	}

	return nil
}

func readJSONValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch token {
	case json.Delim('{'):
		value := map[string]any{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, errors.New("object key must be a string")
			}
			if _, duplicate := value[key]; duplicate {
				return nil, fmt.Errorf("duplicate object key %q", key)
			}
			value[key], err = readJSONValue(decoder)
			if err != nil {
				return nil, err
			}
		}
		_, err = decoder.Token()

		return value, err
	case json.Delim('['):
		value := []any{}
		for decoder.More() {
			item, err := readJSONValue(decoder)
			if err != nil {
				return nil, err
			}
			value = append(value, item)
		}
		_, err = decoder.Token()

		return value, err
	case json.Delim('}'), json.Delim(']'):
		return nil, errors.New("unexpected container delimiter")
	default:
		return token, nil
	}
}

func readYAMLValue(node *yaml.Node) (any, error) {
	switch node.Kind {
	case yaml.DocumentNode, yaml.AliasNode:
		return nil, errors.New("nested YAML documents and aliases are not supported")
	case yaml.MappingNode:
		out := map[string]any{}
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
				return nil, errors.New("object keys must be strings; YAML merge keys are not supported")
			}
			if _, duplicate := out[key.Value]; duplicate {
				return nil, fmt.Errorf("duplicate object key %q", key.Value)
			}
			value, err := readYAMLValue(node.Content[i+1])
			if err != nil {
				return nil, err
			}
			out[key.Value] = value
		}

		return out, nil
	case yaml.SequenceNode:
		out := make([]any, 0, len(node.Content))
		for _, item := range node.Content {
			value, err := readYAMLValue(item)
			if err != nil {
				return nil, err
			}
			out = append(out, value)
		}

		return out, nil
	case yaml.ScalarNode:
		switch node.Tag {
		case "!!null":
			return nil, nil
		case "!!str":
			return node.Value, nil
		case "!!bool":
			return strconv.ParseBool(strings.ToLower(node.Value))
		case "!!int":
			var value int64
			if err := node.Decode(&value); err != nil {
				return nil, errors.New("integer is outside the supported range")
			}

			return value, nil
		case "!!float":
			// No current catalog field accepts fractions. Preserve the type so
			// the JSON decoder rejects it instead of truncating to an integer.
			return json.Number(node.Value), nil
		}
	}

	return nil, errors.New("unsupported YAML node or tag; aliases are not supported")
}
