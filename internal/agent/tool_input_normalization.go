package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"

	"autoto/internal/tools"
)

var strictToolNumberPattern = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$`)

// NormalizeToolInput decodes exactly one JSON object with UseNumber, validates
// the supported schema subset, enforces object boundaries, and normalizes only
// values whose schema explicitly declares integer or number.
func NormalizeToolInput(raw json.RawMessage, schema map[string]any) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode tool input: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, err
	}
	object, ok := decoded.(map[string]any)
	if !ok || object == nil {
		return nil, fmt.Errorf("tool input must be a single JSON object")
	}
	if err := rejectForbiddenToolInputFields(object); err != nil {
		return nil, err
	}
	if err := validateToolInputSchema(schema, "$schema"); err != nil {
		return nil, fmt.Errorf("invalid tool input schema: %w", err)
	}
	if schemaType(schema) != "object" {
		return nil, fmt.Errorf("invalid tool input schema: root type must be object")
	}
	normalized, err := normalizeSchemaObject(object, schema, "$")
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return nil, fmt.Errorf("encode normalized tool input: %w", err)
	}
	return json.RawMessage(encoded), nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("tool input must contain exactly one JSON object")
		}
		return fmt.Errorf("decode trailing tool input: %w", err)
	}
	return nil
}

func rejectForbiddenToolInputFields(object map[string]any) error {
	forbidden := make(map[string]struct{}, len(tools.ForbiddenHostInputFields))
	for _, field := range tools.ForbiddenHostInputFields {
		forbidden[strings.ToLower(strings.TrimSpace(field))] = struct{}{}
	}
	for field := range object {
		if _, denied := forbidden[strings.ToLower(field)]; denied {
			return fmt.Errorf("host field %q is not allowed in tool input; it is injected by the runtime", field)
		}
	}
	return nil
}

func validateToolInputSchema(schema map[string]any, path string) error {
	if schema == nil {
		return fmt.Errorf("%s must be an object schema", path)
	}
	for _, keyword := range []string{"exclusiveMinimum", "exclusiveMaximum"} {
		if _, present := schema[keyword]; present {
			return fmt.Errorf("%s uses unsupported %s", path, keyword)
		}
	}
	for _, keyword := range []string{"anyOf", "allOf", "not", "if", "then", "else", "$ref", "patternProperties"} {
		if _, present := schema[keyword]; present {
			return fmt.Errorf("%s uses unsupported schema keyword %s", path, keyword)
		}
	}
	if rawOneOf, present := schema["oneOf"]; present {
		if _, hasType := schema["type"]; hasType || schema["properties"] != nil || schema["items"] != nil || schema["additionalProperties"] != nil {
			return fmt.Errorf("%s combines oneOf with structural keywords", path)
		}
		branches, ok := rawOneOf.([]any)
		if !ok || len(branches) == 0 {
			return fmt.Errorf("%s oneOf must be a non-empty array", path)
		}
		for index, rawBranch := range branches {
			branch, ok := rawBranch.(map[string]any)
			if !ok {
				return fmt.Errorf("%s.oneOf[%d] must be a schema object", path, index)
			}
			branchType := schemaType(branch)
			if branchType == "" || branchType == "object" || branchType == "array" {
				return fmt.Errorf("%s.oneOf[%d] is too complex for safe normalization", path, index)
			}
			if _, nested := branch["oneOf"]; nested {
				return fmt.Errorf("%s.oneOf[%d] contains nested oneOf", path, index)
			}
			if err := validateToolInputSchema(branch, fmt.Sprintf("%s.oneOf[%d]", path, index)); err != nil {
				return err
			}
		}
		if defaultValue, hasDefault := schema["default"]; hasDefault {
			if _, err := normalizeOneOf(defaultValue, branches, path+".default", false); err != nil {
				return fmt.Errorf("%s has invalid default: %w", path, err)
			}
		}
		return validateConstAndEnumSchema(schema, path)
	}

	typeName := schemaType(schema)
	if rawType, present := schema["type"]; present {
		if _, ok := rawType.(string); !ok {
			return fmt.Errorf("%s type must be a string", path)
		}
		switch typeName {
		case "object", "array", "integer", "number", "string", "boolean", "null":
		default:
			return fmt.Errorf("%s has unsupported type %q", path, typeName)
		}
	}

	_, hasMinimum := schema["minimum"]
	_, hasMaximum := schema["maximum"]
	if (hasMinimum || hasMaximum) && typeName != "integer" && typeName != "number" {
		return fmt.Errorf("%s uses numeric bounds without integer or number type", path)
	}
	if typeName == "integer" || typeName == "number" {
		minimum, hasMinimum, err := schemaNumericBound(schema, "minimum")
		if err != nil {
			return fmt.Errorf("%s has invalid minimum: %w", path, err)
		}
		maximum, hasMaximum, err := schemaNumericBound(schema, "maximum")
		if err != nil {
			return fmt.Errorf("%s has invalid maximum: %w", path, err)
		}
		if hasMinimum && hasMaximum && minimum.rat.Cmp(maximum.rat) > 0 {
			return fmt.Errorf("%s minimum exceeds maximum", path)
		}
		if typeName == "integer" {
			if hasMinimum {
				minimum = integerMinimum(minimum)
			}
			if hasMaximum {
				maximum = integerMaximum(maximum)
			}
			if hasMinimum && hasMaximum && minimum.rat.Cmp(maximum.rat) > 0 {
				return fmt.Errorf("%s has no integer within minimum and maximum", path)
			}
			if hasMinimum && minimum.rat.Num().Cmp(big.NewInt(math.MaxInt64)) > 0 {
				return fmt.Errorf("%s minimum exceeds supported integer range", path)
			}
			if hasMaximum && maximum.rat.Num().Cmp(big.NewInt(math.MinInt64)) < 0 {
				return fmt.Errorf("%s maximum is below supported integer range", path)
			}
		}
	}

	switch typeName {
	case "object":
		properties, err := schemaProperties(schema, path)
		if err != nil {
			return err
		}
		for name, property := range properties {
			if err := validateToolInputSchema(property, path+".properties."+name); err != nil {
				return err
			}
		}
		if rawAdditional, present := schema["additionalProperties"]; present {
			switch additional := rawAdditional.(type) {
			case bool:
			case map[string]any:
				if err := validateToolInputSchema(additional, path+".additionalProperties"); err != nil {
					return err
				}
			default:
				return fmt.Errorf("%s additionalProperties must be boolean or a schema object", path)
			}
		}
		required, err := schemaRequired(schema, path)
		if err != nil {
			return err
		}
		additionalAllowsUnknown := false
		if rawAdditional, present := schema["additionalProperties"]; present {
			switch additional := rawAdditional.(type) {
			case bool:
				additionalAllowsUnknown = additional
			case map[string]any:
				additionalAllowsUnknown = true
			}
		}
		if !additionalAllowsUnknown {
			for _, name := range required {
				if _, declared := properties[name]; !declared {
					return fmt.Errorf("%s requires unknown property %q in a closed object", path, name)
				}
			}
		}
	case "array":
		if rawItems, present := schema["items"]; present {
			items, ok := rawItems.(map[string]any)
			if !ok {
				return fmt.Errorf("%s items must be a schema object", path)
			}
			if err := validateToolInputSchema(items, path+".items"); err != nil {
				return err
			}
		}
	default:
		if _, present := schema["properties"]; present {
			return fmt.Errorf("%s properties require object type", path)
		}
		if _, present := schema["additionalProperties"]; present {
			return fmt.Errorf("%s additionalProperties requires object type", path)
		}
		if _, present := schema["items"]; present {
			return fmt.Errorf("%s items require array type", path)
		}
	}
	if err := validateConstAndEnumSchema(schema, path); err != nil {
		return err
	}
	if defaultValue, hasDefault := schema["default"]; hasDefault {
		if _, err := normalizeSchemaValue(defaultValue, schema, path+".default", false); err != nil {
			return fmt.Errorf("%s has invalid default: %w", path, err)
		}
	}
	return nil
}

func validateConstAndEnumSchema(schema map[string]any, path string) error {
	if rawEnum, present := schema["enum"]; present {
		values, ok := schemaSlice(rawEnum)
		if !ok || len(values) == 0 {
			return fmt.Errorf("%s enum must be a non-empty array", path)
		}
	}
	return nil
}

func schemaProperties(schema map[string]any, path string) (map[string]map[string]any, error) {
	properties := make(map[string]map[string]any)
	rawProperties, present := schema["properties"]
	if !present {
		return properties, nil
	}
	rawMap, ok := rawProperties.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s properties must be an object", path)
	}
	for name, rawProperty := range rawMap {
		property, ok := rawProperty.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s property %q must be a schema object", path, name)
		}
		properties[name] = property
	}
	return properties, nil
}

func schemaRequired(schema map[string]any, path string) ([]string, error) {
	rawRequired, present := schema["required"]
	if !present {
		return nil, nil
	}
	values, ok := schemaSlice(rawRequired)
	if !ok {
		return nil, fmt.Errorf("%s required must be an array", path)
	}
	required := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		name, ok := value.(string)
		if !ok || strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("%s required[%d] must be a non-empty string", path, index)
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, fmt.Errorf("%s required contains duplicate %q", path, name)
		}
		seen[name] = struct{}{}
		required = append(required, name)
	}
	return required, nil
}

func schemaSlice(value any) ([]any, bool) {
	switch typed := value.(type) {
	case []any:
		return typed, true
	case []string:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = item
		}
		return out, true
	default:
		return nil, false
	}
}

func normalizeSchemaObject(object map[string]any, schema map[string]any, path string) (map[string]any, error) {
	properties, err := schemaProperties(schema, path)
	if err != nil {
		return nil, err
	}
	out := make(map[string]any, len(object))
	for name, value := range object {
		propertyPath := path + "." + name
		if property, declared := properties[name]; declared {
			normalized, err := normalizeSchemaValue(value, property, propertyPath, true)
			if err != nil {
				return nil, err
			}
			out[name] = normalized
			continue
		}
		rawAdditional, explicit := schema["additionalProperties"]
		if !explicit {
			return nil, fmt.Errorf("%s contains unknown property %q", path, name)
		}
		switch additional := rawAdditional.(type) {
		case bool:
			if !additional {
				return nil, fmt.Errorf("%s contains unknown property %q", path, name)
			}
			out[name] = value
		case map[string]any:
			normalized, err := normalizeSchemaValue(value, additional, propertyPath, true)
			if err != nil {
				return nil, err
			}
			out[name] = normalized
		default:
			return nil, fmt.Errorf("%s has invalid additionalProperties schema", path)
		}
	}
	for name, property := range properties {
		if _, present := out[name]; present {
			continue
		}
		defaultValue, hasDefault := property["default"]
		if !hasDefault || !schemaHasNumericTarget(property) {
			continue
		}
		normalizedDefault, err := normalizeSchemaValue(defaultValue, property, path+"."+name, false)
		if err != nil {
			return nil, fmt.Errorf("invalid default for %s.%s: %w", path, name, err)
		}
		out[name] = normalizedDefault
	}
	required, err := schemaRequired(schema, path)
	if err != nil {
		return nil, err
	}
	for _, name := range required {
		if _, present := out[name]; !present {
			return nil, fmt.Errorf("%s is missing required property %q", path, name)
		}
	}
	return out, nil
}

func normalizeSchemaValue(value any, schema map[string]any, path string, clamp bool) (any, error) {
	if branches, ok := schema["oneOf"].([]any); ok {
		return normalizeOneOf(value, branches, path, clamp)
	}
	var normalized any
	switch schemaType(schema) {
	case "":
		normalized = value
	case "integer", "number":
		value, err := normalizeNumericValue(value, schema, path, clamp)
		if err != nil {
			return nil, err
		}
		normalized = value
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s must be an object", path)
		}
		value, err := normalizeSchemaObject(object, schema, path)
		if err != nil {
			return nil, err
		}
		normalized = value
	case "array":
		values, ok := value.([]any)
		if !ok {
			return nil, fmt.Errorf("%s must be an array", path)
		}
		rawItems, hasItems := schema["items"]
		if !hasItems {
			normalized = values
			break
		}
		items := rawItems.(map[string]any)
		out := make([]any, len(values))
		for index, item := range values {
			normalizedItem, err := normalizeSchemaValue(item, items, fmt.Sprintf("%s[%d]", path, index), clamp)
			if err != nil {
				return nil, err
			}
			out[index] = normalizedItem
		}
		normalized = out
	case "string":
		if _, ok := value.(string); !ok {
			return nil, fmt.Errorf("%s must be a string", path)
		}
		normalized = value
	case "boolean":
		if _, ok := value.(bool); !ok {
			return nil, fmt.Errorf("%s must be a boolean", path)
		}
		normalized = value
	case "null":
		if value != nil {
			return nil, fmt.Errorf("%s must be null", path)
		}
		normalized = nil
	default:
		return nil, fmt.Errorf("%s has unsupported schema type", path)
	}
	if err := validateSchemaValueConstraints(normalized, schema, path); err != nil {
		return nil, err
	}
	return normalized, nil
}

func normalizeOneOf(value any, branches []any, path string, clamp bool) (any, error) {
	candidates := make([]any, 0, len(branches))
	var firstErr error
	for _, rawBranch := range branches {
		branch, ok := rawBranch.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s has invalid oneOf branch", path)
		}
		normalized, err := normalizeSchemaValueWithoutOneOf(value, branch, path, clamp)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		candidates = append(candidates, normalized)
	}
	if len(candidates) > 1 {
		return nil, fmt.Errorf("%s matches multiple oneOf branches; normalization is ambiguous", path)
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	if firstErr != nil {
		return nil, fmt.Errorf("%s does not match exactly one oneOf branch: %w", path, firstErr)
	}
	return nil, fmt.Errorf("%s does not match exactly one oneOf branch", path)
}

func normalizeSchemaValueWithoutOneOf(value any, schema map[string]any, path string, clamp bool) (any, error) {
	copySchema := make(map[string]any, len(schema))
	for key, item := range schema {
		if key != "oneOf" && key != "default" {
			copySchema[key] = item
		}
	}
	return normalizeSchemaValue(value, copySchema, path, clamp)
}

func schemaHasNumericTarget(schema map[string]any) bool {
	typeName := schemaType(schema)
	if typeName == "integer" || typeName == "number" {
		return true
	}
	if branches, ok := schema["oneOf"].([]any); ok {
		for _, rawBranch := range branches {
			if branch, ok := rawBranch.(map[string]any); ok && schemaHasNumericTarget(branch) {
				return true
			}
		}
	}
	return false
}

func schemaType(schema map[string]any) string {
	typeName, _ := schema["type"].(string)
	return strings.TrimSpace(typeName)
}

type parsedToolNumber struct {
	rat     *big.Rat
	literal string
}

func normalizeNumericValue(value any, schema map[string]any, path string, clamp bool) (any, error) {
	number, err := parseToolNumber(value)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	integer := schemaType(schema) == "integer"
	if integer && !number.rat.IsInt() {
		return nil, fmt.Errorf("%s must be an integer", path)
	}
	if integer && !fitsSignedInt64(number.rat.Num()) {
		return nil, fmt.Errorf("%s integer overflows int64", path)
	}
	minimum, hasMinimum, err := schemaNumericBound(schema, "minimum")
	if err != nil {
		return nil, fmt.Errorf("%s has invalid minimum: %w", path, err)
	}
	maximum, hasMaximum, err := schemaNumericBound(schema, "maximum")
	if err != nil {
		return nil, fmt.Errorf("%s has invalid maximum: %w", path, err)
	}
	if integer {
		if hasMinimum {
			minimum = integerMinimum(minimum)
		}
		if hasMaximum {
			maximum = integerMaximum(maximum)
		}
	}
	if hasMinimum && number.rat.Cmp(minimum.rat) < 0 {
		if !clamp {
			return nil, fmt.Errorf("%s default is below minimum", path)
		}
		number = minimum
	}
	if hasMaximum && number.rat.Cmp(maximum.rat) > 0 {
		if !clamp {
			return nil, fmt.Errorf("%s default is above maximum", path)
		}
		number = maximum
	}
	if integer {
		if !fitsSignedInt64(number.rat.Num()) {
			return nil, fmt.Errorf("%s integer overflows int64", path)
		}
		return json.Number(number.rat.Num().String()), nil
	}
	return json.Number(number.literal), nil
}

func parseToolNumber(value any) (parsedToolNumber, error) {
	var literal string
	switch typed := value.(type) {
	case json.Number:
		literal = typed.String()
	case string:
		literal = typed
	case int:
		literal = strconv.FormatInt(int64(typed), 10)
	case int8:
		literal = strconv.FormatInt(int64(typed), 10)
	case int16:
		literal = strconv.FormatInt(int64(typed), 10)
	case int32:
		literal = strconv.FormatInt(int64(typed), 10)
	case int64:
		literal = strconv.FormatInt(typed, 10)
	case uint:
		literal = strconv.FormatUint(uint64(typed), 10)
	case uint8:
		literal = strconv.FormatUint(uint64(typed), 10)
	case uint16:
		literal = strconv.FormatUint(uint64(typed), 10)
	case uint32:
		literal = strconv.FormatUint(uint64(typed), 10)
	case uint64:
		literal = strconv.FormatUint(typed, 10)
	case float32:
		if math.IsNaN(float64(typed)) || math.IsInf(float64(typed), 0) {
			return parsedToolNumber{}, fmt.Errorf("number must be finite")
		}
		literal = strconv.FormatFloat(float64(typed), 'g', -1, 32)
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return parsedToolNumber{}, fmt.Errorf("number must be finite")
		}
		literal = strconv.FormatFloat(typed, 'g', -1, 64)
	default:
		return parsedToolNumber{}, fmt.Errorf("expected a JSON number or strict numeric string")
	}
	if !strictToolNumberPattern.MatchString(literal) {
		return parsedToolNumber{}, fmt.Errorf("%q is not a strict numeric string", literal)
	}
	parsedFloat, err := strconv.ParseFloat(literal, 64)
	if err != nil || math.IsNaN(parsedFloat) || math.IsInf(parsedFloat, 0) {
		return parsedToolNumber{}, fmt.Errorf("%q is not a finite JSON number", literal)
	}
	rat, ok := new(big.Rat).SetString(literal)
	if !ok {
		return parsedToolNumber{}, fmt.Errorf("%q is not a valid JSON number", literal)
	}
	return parsedToolNumber{rat: rat, literal: literal}, nil
}

func schemaNumericBound(schema map[string]any, key string) (parsedToolNumber, bool, error) {
	value, ok := schema[key]
	if !ok {
		return parsedToolNumber{}, false, nil
	}
	parsed, err := parseToolNumber(value)
	if err != nil {
		return parsedToolNumber{}, false, err
	}
	return parsed, true, nil
}

func integerMinimum(bound parsedToolNumber) parsedToolNumber {
	value := ceilRat(bound.rat)
	return parsedToolNumber{rat: new(big.Rat).SetInt(value), literal: value.String()}
}

func integerMaximum(bound parsedToolNumber) parsedToolNumber {
	value := floorRat(bound.rat)
	return parsedToolNumber{rat: new(big.Rat).SetInt(value), literal: value.String()}
}

func floorRat(value *big.Rat) *big.Int {
	quotient := new(big.Int).Quo(value.Num(), value.Denom())
	if value.Sign() < 0 && new(big.Int).Rem(value.Num(), value.Denom()).Sign() != 0 {
		quotient.Sub(quotient, big.NewInt(1))
	}
	return quotient
}

func ceilRat(value *big.Rat) *big.Int {
	quotient := new(big.Int).Quo(value.Num(), value.Denom())
	if value.Sign() > 0 && new(big.Int).Rem(value.Num(), value.Denom()).Sign() != 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	return quotient
}

func fitsSignedInt64(value *big.Int) bool {
	return value.Cmp(big.NewInt(math.MinInt64)) >= 0 && value.Cmp(big.NewInt(math.MaxInt64)) <= 0
}

func validateSchemaValueConstraints(value any, schema map[string]any, path string) error {
	if expected, ok := schema["const"]; ok && !schemaValuesEqual(value, expected, schema) {
		return fmt.Errorf("%s does not match const", path)
	}
	if rawEnum, ok := schema["enum"]; ok {
		values, valid := schemaSlice(rawEnum)
		if !valid {
			return fmt.Errorf("%s has invalid enum", path)
		}
		for _, candidate := range values {
			if schemaValuesEqual(value, candidate, schema) {
				return nil
			}
		}
		return fmt.Errorf("%s is not an allowed enum value", path)
	}
	return nil
}

func schemaValuesEqual(left, right any, schema map[string]any) bool {
	if schemaType(schema) == "integer" || schemaType(schema) == "number" {
		leftNumber, leftErr := parseToolNumber(left)
		rightNumber, rightErr := parseToolNumber(right)
		return leftErr == nil && rightErr == nil && leftNumber.rat.Cmp(rightNumber.rat) == 0
	}
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}
