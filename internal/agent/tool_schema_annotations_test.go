package agent

import (
	"testing"

	"autoto/internal/tools"
)

type annotatedSchemaFixture struct {
	Required string `json:"required" desc:"A required field, described."`
	Mode     string `json:"mode,omitempty" jsonschema:"enum=fast|slow" desc:"How to run."`
	Count    int    `json:"count,omitempty" jsonschema:"minimum=1,maximum=10" desc:"How many."`
	Name     string `json:"name,omitempty" jsonschema:"minLength=1,maxLength=64"`
	Plain    bool   `json:"plain,omitempty"`
	// Constraints that do not apply to the field's type must be ignored rather
	// than emitted, because the validator rejects them and one mis-tagged field
	// would otherwise break the whole tool catalog.
	Mistagged string `json:"mistagged,omitempty" jsonschema:"minimum=5"`
}

func fixtureProperty(t *testing.T, schema map[string]any, name string) map[string]any {
	t.Helper()
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema has no properties: %+v", schema)
	}
	property, ok := properties[name].(map[string]any)
	if !ok {
		t.Fatalf("schema has no property %q: %+v", name, properties)
	}
	return property
}

func TestToolSchemaCarriesFieldAnnotations(t *testing.T) {
	schema, err := checkedToolInputSchema(annotatedSchemaFixture{})
	if err != nil {
		t.Fatalf("annotated schema must stay valid: %v", err)
	}

	t.Run("descriptions", func(t *testing.T) {
		if got := fixtureProperty(t, schema, "required")["description"]; got != "A required field, described." {
			t.Fatalf("description missing or wrong: %v", got)
		}
		if _, present := fixtureProperty(t, schema, "plain")["description"]; present {
			t.Fatalf("an untagged field must not gain a description")
		}
	})

	t.Run("numeric bounds", func(t *testing.T) {
		count := fixtureProperty(t, schema, "count")
		if count["minimum"] != float64(1) || count["maximum"] != float64(10) {
			t.Fatalf("expected numeric bounds, got %+v", count)
		}
	})

	t.Run("string lengths", func(t *testing.T) {
		name := fixtureProperty(t, schema, "name")
		if name["minLength"] != 1 || name["maxLength"] != 64 {
			t.Fatalf("expected string length bounds, got %+v", name)
		}
	})

	t.Run("mismatched constraints are dropped", func(t *testing.T) {
		mistagged := fixtureProperty(t, schema, "mistagged")
		if _, present := mistagged["minimum"]; present {
			t.Fatalf("minimum must not be emitted for a string field: %+v", mistagged)
		}
	})
}

// TestOptionalStringEnumAcceptsEmpty covers a real failure mode: models send ""
// for a parameter they mean to omit. An optional enum field must tolerate that
// instead of hard-failing input validation.
func TestOptionalStringEnumAcceptsEmpty(t *testing.T) {
	schema, err := checkedToolInputSchema(annotatedSchemaFixture{})
	if err != nil {
		t.Fatal(err)
	}
	values, ok := fixtureProperty(t, schema, "mode")["enum"].([]any)
	if !ok {
		t.Fatalf("expected an enum on the optional mode field")
	}
	found := map[string]bool{}
	for _, value := range values {
		if text, ok := value.(string); ok {
			found[text] = true
		}
	}
	for _, want := range []string{"fast", "slow", ""} {
		if !found[want] {
			t.Fatalf("optional enum must include %q, got %v", want, values)
		}
	}
}

type requiredEnumFixture struct {
	Action string `json:"action" jsonschema:"enum=list|cancel"`
}

func TestRequiredEnumDoesNotAcceptEmpty(t *testing.T) {
	schema, err := checkedToolInputSchema(requiredEnumFixture{})
	if err != nil {
		t.Fatal(err)
	}
	values, _ := fixtureProperty(t, schema, "action")["enum"].([]any)
	for _, value := range values {
		if value == "" {
			t.Fatalf("a required enum must not accept the empty string: %v", values)
		}
	}
	if len(values) != 2 {
		t.Fatalf("expected exactly the two declared values, got %v", values)
	}
}

// TestRegisteredToolSchemasStayValid is the guard that matters for the catalog:
// annotating a tool must never produce a schema the runner refuses, because a
// single invalid schema fails the whole snapshot.
func TestRegisteredToolSchemasStayValid(t *testing.T) {
	registry := tools.NewRegistry()
	tools.RegisterCore(registry)
	for _, tool := range registry.List() {
		if _, err := checkedToolInputSchema(tool.Schema()); err != nil {
			t.Errorf("tool %s has an invalid schema: %v", tool.Name(), err)
		}
	}
}

// TestCoreToolsDocumentTheirInputs keeps the annotations from silently rotting
// away: the tools an agent uses constantly must describe their parameters.
func TestCoreToolsDocumentTheirInputs(t *testing.T) {
	registry := tools.NewRegistry()
	tools.RegisterCore(registry)
	mustDocument := map[string]bool{"Read": true, "Write": true, "Edit": true, "MultiEdit": true, "Bash": true, "Glob": true, "Grep": true, "LS": true, "TodoWrite": true, "Symbols": true, "Task": true, "Agent": true, "WebFetch": true, "WebSearch": true}
	for _, tool := range registry.List() {
		if !mustDocument[tool.Name()] {
			continue
		}
		schema, err := checkedToolInputSchema(tool.Schema())
		if err != nil {
			t.Errorf("tool %s has an invalid schema: %v", tool.Name(), err)
			continue
		}
		properties, ok := schema["properties"].(map[string]any)
		if !ok || len(properties) == 0 {
			t.Errorf("tool %s exposes no properties", tool.Name())
			continue
		}
		for name, raw := range properties {
			property, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if description, _ := property["description"].(string); description == "" {
				t.Errorf("tool %s parameter %q has no description", tool.Name(), name)
			}
		}
	}
}
