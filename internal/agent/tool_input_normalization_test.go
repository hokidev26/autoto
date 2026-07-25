package agent

import (
	"bytes"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeToolInputNormalizesOnlyExplicitNumericProperties(t *testing.T) {
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": true,
		"properties": map[string]any{
			"count": map[string]any{"type": "integer"},
			"ratio": map[string]any{"type": "number", "minimum": 3, "maximum": 5},
			"nested": map[string]any{
				"type":                 "object",
				"additionalProperties": true,
				"properties": map[string]any{

					"limit": map[string]any{"type": "integer"},
				},
			},
			"items": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type":                 "object",
					"properties":           map[string]any{"score": map[string]any{"type": "number"}},
					"additionalProperties": true,
				},
			},
			"open": map[string]any{"type": "object", "additionalProperties": true},
		},
	}
	raw := json.RawMessage(`{"count":"3.0","ratio":"9","unknown":"7","nested":{"limit":"4e0","other":"8"},"items":[{"score":"2.5","unknown":"6"}],"open":{"value":"10"}}`)

	normalized, err := NormalizeToolInput(raw, schema)
	if err != nil {
		t.Fatal(err)
	}
	got := decodeUseNumberObject(t, normalized)
	want := decodeUseNumberObject(t, json.RawMessage(`{"count":3,"ratio":5,"unknown":"7","nested":{"limit":4,"other":"8"},"items":[{"score":2.5,"unknown":"6"}],"open":{"value":"10"}}`))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected normalized input:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestNormalizeToolInputAppliesOnlyLegalNumericDefaults(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"count": map[string]any{"type": "integer", "default": "3.0", "minimum": 1, "maximum": 5},
			"ratio": map[string]any{"type": "number", "default": json.Number("2.5")},
			"label": map[string]any{"type": "string", "default": "not-applied"},
		},
	}
	normalized, err := NormalizeToolInput(json.RawMessage(`{}`), schema)
	if err != nil {
		t.Fatal(err)
	}
	got := decodeUseNumberObject(t, normalized)
	want := decodeUseNumberObject(t, json.RawMessage(`{"count":3,"ratio":2.5}`))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected defaults: got=%#v want=%#v", got, want)
	}

	for name, property := range map[string]map[string]any{
		"unit":     {"type": "integer", "default": "3ms"},
		"fraction": {"type": "integer", "default": "3.2"},
		"bounded":  {"type": "number", "default": 9, "maximum": 5},
		"nan":      {"type": "number", "default": math.NaN()},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NormalizeToolInput(json.RawMessage(`{}`), map[string]any{"type": "object", "properties": map[string]any{"value": property}})
			if err == nil || !strings.Contains(err.Error(), "invalid default") {
				t.Fatalf("expected invalid default error, got %v", err)
			}
		})
	}
}

func TestNormalizeToolInputClampsMinimumAndMaximum(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"low":   map[string]any{"type": "integer", "minimum": 3.2},
			"high":  map[string]any{"type": "integer", "maximum": 3.8},
			"ratio": map[string]any{"type": "number", "minimum": json.Number("0.25"), "maximum": json.Number("0.75")},
		},
	}
	normalized, err := NormalizeToolInput(json.RawMessage(`{"low":1,"high":9,"ratio":"0.9"}`), schema)
	if err != nil {
		t.Fatal(err)
	}
	got := decodeUseNumberObject(t, normalized)
	want := decodeUseNumberObject(t, json.RawMessage(`{"low":4,"high":3,"ratio":0.75}`))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected clamping: got=%#v want=%#v", got, want)
	}
}

func TestNormalizeToolInputRejectsInvalidNumericValues(t *testing.T) {
	integerSchema := map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "integer"}}}
	numberSchema := map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "number"}}}
	cases := []struct {
		name   string
		raw    json.RawMessage
		schema map[string]any
	}{
		{name: "fractional integer", raw: json.RawMessage(`{"value":"3.2"}`), schema: integerSchema},
		{name: "unit", raw: json.RawMessage(`{"value":"3ms"}`), schema: numberSchema},
		{name: "leading plus", raw: json.RawMessage(`{"value":"+3"}`), schema: numberSchema},
		{name: "leading zero", raw: json.RawMessage(`{"value":"03"}`), schema: numberSchema},
		{name: "whitespace", raw: json.RawMessage(`{"value":" 3 "}`), schema: numberSchema},
		{name: "nan", raw: json.RawMessage(`{"value":"NaN"}`), schema: numberSchema},
		{name: "infinity", raw: json.RawMessage(`{"value":"Inf"}`), schema: numberSchema},
		{name: "float overflow", raw: json.RawMessage(`{"value":"1e10000"}`), schema: numberSchema},
		{name: "integer overflow", raw: json.RawMessage(`{"value":"9223372036854775808"}`), schema: integerSchema},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NormalizeToolInput(test.raw, test.schema); err == nil {
				t.Fatal("expected normalization to fail")
			}
		})
	}
}

func TestNormalizeToolInputRejectsAmbiguousNonNumericOneOf(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"value": map[string]any{
				"oneOf": []any{
					map[string]any{"type": "string"},
					map[string]any{"type": "string", "const": "3"},
				},
			},
		},
	}
	if _, err := NormalizeToolInput(json.RawMessage(`{"value":"3"}`), schema); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected ambiguous non-numeric oneOf error, got %v", err)
	}
}

func TestNormalizeToolInputRejectsAmbiguousOneOf(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"value": map[string]any{
				"oneOf": []any{
					map[string]any{"type": "integer"},
					map[string]any{"type": "number"},
				},
			},
		},
	}
	if _, err := NormalizeToolInput(json.RawMessage(`{"value":"3"}`), schema); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected ambiguous oneOf error, got %v", err)
	}
}

func TestNormalizeToolInputEnforcesClosedWorldRecursively(t *testing.T) {
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"nested": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties":           map[string]any{"count": map[string]any{"type": "integer"}},
			},
		},
	}
	for name, raw := range map[string]json.RawMessage{
		"root":   json.RawMessage(`{"unknown":1}`),
		"nested": json.RawMessage(`{"nested":{"count":1,"unknown":2}}`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NormalizeToolInput(raw, schema); err == nil || !strings.Contains(err.Error(), "unknown property") {
				t.Fatalf("expected closed-world rejection, got %v", err)
			}
		})
	}
}

func TestNormalizeToolInputHonorsAdditionalPropertiesModes(t *testing.T) {
	t.Run("true preserves unknown values", func(t *testing.T) {
		schema := map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": true}
		normalized, err := NormalizeToolInput(json.RawMessage(`{"value":"3.0"}`), schema)
		if err != nil {
			t.Fatal(err)
		}
		if string(normalized) != `{"value":"3.0"}` {
			t.Fatalf("additionalProperties true changed unknown value: %s", normalized)
		}
	})
	t.Run("schema normalizes unknown values", func(t *testing.T) {
		schema := map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"additionalProperties": map[string]any{"type": "integer", "minimum": 2, "maximum": 4},
		}
		normalized, err := NormalizeToolInput(json.RawMessage(`{"low":"1","exact":"3.0","high":"9"}`), schema)
		if err != nil {
			t.Fatal(err)
		}
		want := `{"exact":3,"high":4,"low":2}`
		if string(normalized) != want {
			t.Fatalf("additionalProperties schema mismatch: got=%s want=%s", normalized, want)
		}
	})
}

func TestNormalizeToolInputRejectsForbiddenHostFieldsBeforeToolRisk(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": true}
	for _, field := range []string{"agentId", "AGENT_ID", "cwd", "WorkingDirectory", "project_id"} {
		raw, err := json.Marshal(map[string]any{field: "attacker-controlled"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := NormalizeToolInput(raw, schema); err == nil || !strings.Contains(err.Error(), "host field") {
			t.Fatalf("expected forbidden host field %q rejection, got %v", field, err)
		}
	}
}

func TestNormalizeToolInputFailsClosedOnInvalidSchema(t *testing.T) {
	cases := map[string]map[string]any{
		"reversed bounds": {
			"type": "object", "properties": map[string]any{"value": map[string]any{"type": "number", "minimum": 5, "maximum": 1}},
		},
		"invalid bound": {
			"type": "object", "properties": map[string]any{"value": map[string]any{"type": "number", "minimum": "3ms"}},
		},
		"empty integer interval": {
			"type": "object", "properties": map[string]any{"value": map[string]any{"type": "integer", "minimum": 3.2, "maximum": 3.8}},
		},
		"exclusive bound": {
			"type": "object", "properties": map[string]any{"value": map[string]any{"type": "number", "exclusiveMinimum": 1}},
		},
		"invalid default type": {
			"type": "object", "properties": map[string]any{"value": map[string]any{"type": "integer", "default": true}},
		},
		"invalid additional properties": {
			"type": "object", "additionalProperties": "yes",
		},
		"closed required unknown property": {
			"type": "object", "properties": map[string]any{}, "additionalProperties": false, "required": []string{"missing"},
		},
		"complex oneOf": {
			"type": "object", "properties": map[string]any{"value": map[string]any{"oneOf": []any{map[string]any{"type": "object"}}}},
		},
		"unsupported ref": {
			"type": "object", "properties": map[string]any{"value": map[string]any{"$ref": "#/$defs/value"}},
		},
	}
	for name, schema := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NormalizeToolInput(json.RawMessage(`{}`), schema); err == nil || !strings.Contains(err.Error(), "invalid tool input schema") {
				t.Fatalf("expected schema rejection, got %v", err)
			}
		})
	}
}

func TestNormalizeToolInputEnforcesRequiredAfterNumericDefaults(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"with_default": map[string]any{"type": "integer", "default": "3.0"},
			"missing":      map[string]any{"type": "string"},
		},
		"required": []string{"with_default", "missing"},
	}
	if _, err := NormalizeToolInput(json.RawMessage(`{}`), schema); err == nil || !strings.Contains(err.Error(), "missing required") {
		t.Fatalf("expected required property rejection, got %v", err)
	}
}

func TestNormalizeToolInputRequiresExactlyOneJSONObject(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{}}
	for _, raw := range []json.RawMessage{
		nil,
		json.RawMessage(``),
		json.RawMessage(`null`),
		json.RawMessage(`[]`),
		json.RawMessage(`{"value":1`),
		json.RawMessage(`{"value":1} {}`),
	} {
		if normalized, err := NormalizeToolInput(raw, schema); err == nil {
			t.Fatalf("expected strict object error for %q, got %s", raw, normalized)
		}
	}
}

func decodeUseNumberObject(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil {
		t.Fatal(err)
	}
	return object
}
