package agent

import (
	"encoding/json"
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
	return schema
}

func nativeToolInputSchema(input any) (map[string]any, bool) {
	var raw []byte
	switch schema := input.(type) {
	case json.RawMessage:
		raw = schema
	case []byte:
		raw = schema
	case map[string]any:
		encoded, err := json.Marshal(schema)
		if err != nil {
			return nil, false
		}
		raw = encoded
	case *json.RawMessage:
		if schema == nil {
			return nil, false
		}
		raw = *schema
	default:
		return nil, false
	}
	var decoded map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil || decoded == nil {
		return nil, false
	}
	return decoded, true
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
