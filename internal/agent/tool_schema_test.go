package agent

import (
	"encoding/json"
	"testing"
)

func TestCheckedToolInputSchemaRejectsInvalidNativeSchemas(t *testing.T) {
	cases := map[string]json.RawMessage{
		"malformed":          json.RawMessage(`{"type":"object"`),
		"trailing":           json.RawMessage(`{"type":"object"} {}`),
		"non-object":         json.RawMessage(`[]`),
		"reversed bounds":    json.RawMessage(`{"type":"object","properties":{"value":{"type":"number","minimum":2,"maximum":1}}}`),
		"exclusive bounds":   json.RawMessage(`{"type":"object","properties":{"value":{"type":"number","exclusiveMaximum":3}}}`),
		"complex oneOf":      json.RawMessage(`{"type":"object","properties":{"value":{"oneOf":[{"type":"object"}]}}}`),
		"invalid additional": json.RawMessage(`{"type":"object","additionalProperties":"yes"}`),
	}
	for name, schema := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := checkedToolInputSchema(schema); err == nil {
				t.Fatal("expected invalid native schema rejection")
			}
		})
	}
}

func TestCheckedToolInputSchemaClosesAdditionalPropertyObjectSchemas(t *testing.T) {
	schema, err := checkedToolInputSchema(json.RawMessage(`{"type":"object","properties":{},"additionalProperties":{"type":"object","properties":{"count":{"type":"integer"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	additional := schema["additionalProperties"].(map[string]any)
	if additional["additionalProperties"] != false {
		t.Fatalf("nested additionalProperties schema was not closed: %+v", schema)
	}
}

func TestToolInputSchemaTreatsRawMessageAsObject(t *testing.T) {
	type input struct {
		Arguments json.RawMessage `json:"arguments,omitempty"`
		Blob      []byte          `json:"blob,omitempty"`
	}
	schema := toolInputSchema(input{})
	properties := schema["properties"].(map[string]any)
	arguments := properties["arguments"].(map[string]any)
	if arguments["type"] != "object" || arguments["additionalProperties"] != true {
		t.Fatalf("json.RawMessage must be advertised as an open object: %+v", arguments)
	}
	blob := properties["blob"].(map[string]any)
	if blob["type"] != "string" {
		t.Fatalf("plain []byte must stay a string: %+v", blob)
	}
}

func TestToolInputSchemaBuildsNestedObjectsAndArrays(t *testing.T) {
	type child struct {
		Name string `json:"name"`
	}
	type input struct {
		Child    child           `json:"child"`
		Children []child         `json:"children,omitempty"`
		Options  map[string]bool `json:"options,omitempty"`
	}
	schema := toolInputSchema(input{})
	properties := schema["properties"].(map[string]any)
	childSchema := properties["child"].(map[string]any)
	if childSchema["type"] != "object" || childSchema["properties"].(map[string]any)["name"].(map[string]any)["type"] != "string" {
		t.Fatalf("nested struct schema was not recursive: %+v", schema)
	}
	childrenSchema := properties["children"].(map[string]any)
	if childrenSchema["type"] != "array" || childrenSchema["items"].(map[string]any)["type"] != "object" {
		t.Fatalf("nested array schema was not recursive: %+v", schema)
	}
	if properties["options"].(map[string]any)["type"] != "object" {
		t.Fatalf("map schema should remain an object: %+v", schema)
	}
}
