package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

func toolInputSchema(input any) map[string]any {
	if schema, ok := nativeToolInputSchema(input); ok {
		return enforceClosedWorldObjectSchema(schema)
	}
	t := reflect.TypeOf(input)
	if t == nil {
		return map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
	}
	schema := jsonSchemaForType(t, make(map[reflect.Type]bool))
	if schema["type"] != "object" {
		return map[string]any{"type": "object", "properties": map[string]any{"input": schema}, "required": []string{"input"}, "additionalProperties": false}
	}
	return enforceClosedWorldObjectSchema(schema)
}

// enforceClosedWorldObjectSchema pins object schemas to reject unknown properties
// unless a schema author explicitly set additionalProperties.
func enforceClosedWorldObjectSchema(schema map[string]any) map[string]any {
	if schema == nil {
		return map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
	}
	if _, ok := schema["additionalProperties"]; !ok && schema["type"] == "object" {
		schema["additionalProperties"] = false
	}
	if properties, ok := schema["properties"].(map[string]any); ok {
		for key, value := range properties {
			if nested, ok := value.(map[string]any); ok {
				properties[key] = enforceClosedWorldObjectSchema(nested)
			}
		}
	}
	if items, ok := schema["items"].(map[string]any); ok {
		schema["items"] = enforceClosedWorldObjectSchema(items)
	}
	if additional, ok := schema["additionalProperties"].(map[string]any); ok {
		schema["additionalProperties"] = enforceClosedWorldObjectSchema(additional)
	}
	if branches, ok := schema["oneOf"].([]any); ok {
		for index, rawBranch := range branches {
			if branch, ok := rawBranch.(map[string]any); ok {
				branches[index] = enforceClosedWorldObjectSchema(branch)
			}
		}
		schema["oneOf"] = branches
	}
	return schema
}

func checkedToolInputSchema(input any) (map[string]any, error) {
	if schema, native, err := decodeNativeToolInputSchema(input); native {
		if err != nil {
			return nil, err
		}
		schema = enforceClosedWorldObjectSchema(schema)
		if err := validateToolInputSchema(schema, "$schema"); err != nil {
			return nil, err
		}
		return schema, nil
	}
	schema := toolInputSchema(input)
	if err := validateToolInputSchema(schema, "$schema"); err != nil {
		return nil, err
	}
	return schema, nil
}

func nativeToolInputSchema(input any) (map[string]any, bool) {
	schema, native, err := decodeNativeToolInputSchema(input)
	return schema, native && err == nil
}

func decodeNativeToolInputSchema(input any) (map[string]any, bool, error) {
	var raw []byte
	switch schema := input.(type) {
	case json.RawMessage:
		raw = schema
	case []byte:
		raw = schema
	case map[string]any:
		encoded, err := json.Marshal(schema)
		if err != nil {
			return nil, true, fmt.Errorf("encode native tool schema: %w", err)
		}
		raw = encoded
	case *json.RawMessage:
		if schema == nil {
			return nil, true, fmt.Errorf("native tool schema is nil")
		}
		raw = *schema
	default:
		return nil, false, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, true, fmt.Errorf("decode native tool schema: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, true, fmt.Errorf("decode native tool schema: %w", err)
	}
	schema, ok := decoded.(map[string]any)
	if !ok || schema == nil {
		return nil, true, fmt.Errorf("native tool schema must be a single JSON object")
	}
	return schema, true, nil
}

func jsonSchemaForType(t reflect.Type, visiting map[reflect.Type]bool) map[string]any {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if visiting[t] {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	switch t.Kind() {
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return map[string]any{"type": "string"}
		}
		return map[string]any{"type": "array", "items": jsonSchemaForType(t.Elem(), visiting)}
	case reflect.Map:
		// Open maps remain open-ended; callers that need closed-world must use structs.
		return map[string]any{"type": "object", "additionalProperties": true}
	case reflect.Struct:
		visiting[t] = true
		defer delete(visiting, t)
		properties := make(map[string]any)
		required := make([]string, 0)
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			if field.PkgPath != "" {
				continue
			}
			name, omitEmpty := jsonFieldName(field)
			if name == "" {
				continue
			}
			properties[name] = jsonSchemaForType(field.Type, visiting)
			if !omitEmpty {
				required = append(required, name)
			}
		}
		schema := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
		if len(required) > 0 {
			schema["required"] = required
		}
		return schema
	default:
		return map[string]any{"type": "string"}
	}
}

func jsonFieldName(field reflect.StructField) (string, bool) {
	name := field.Name
	omitEmpty := false
	if tag := field.Tag.Get("json"); tag != "" {
		parts := strings.Split(tag, ",")
		if parts[0] == "-" {
			return "", false
		}
		if parts[0] != "" {
			name = parts[0]
		}
		for _, part := range parts[1:] {
			if part == "omitempty" {
				omitEmpty = true
			}
		}
	}
	return name, omitEmpty
}
