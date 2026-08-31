package llm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"strconv"
	"strings"
)

// ValidateJSONSchema validates one complete JSON value against a deliberately
// small strict-schema subset used by provider responses. It rejects trailing
// JSON and supports object, array, scalar types, required fields,
// additionalProperties, properties, enum, const, numeric bounds, and items.
func ValidateJSONSchema(data, schema json.RawMessage) error {
	if len(schema) == 0 {
		schema = signalSchema
	}
	var value any
	if err := decodeSingleJSON(data, &value); err != nil {
		return fmt.Errorf("%w: %v", ErrSchemaViolation, err)
	}
	var document any
	if err := decodeSingleJSON(schema, &document); err != nil {
		return fmt.Errorf("%w: invalid schema: %v", ErrSchemaViolation, err)
	}
	schemaObject, ok := document.(map[string]any)
	if !ok || schemaObject == nil {
		return fmt.Errorf("%w: schema must be an object", ErrSchemaViolation)
	}
	if err := validateSchemaValue(value, schemaObject, "$", true); err != nil {
		return fmt.Errorf("%w: %v", ErrSchemaViolation, err)
	}
	return nil
}

func decodeSingleJSON(data []byte, output *any) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return errors.New("empty JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("trailing JSON")
		}
		return fmt.Errorf("trailing JSON: %w", err)
	}
	return nil
}

func validateSchemaValue(value any, schema map[string]any, path string, root bool) error {
	if schema == nil {
		return fmt.Errorf("%s: schema must be an object", path)
	}
	if typeValue, exists := schema["type"]; exists {
		types, err := schemaTypes(typeValue)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		matched := false
		for _, typeName := range types {
			if matchesJSONType(value, typeName) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s: expected %s", path, strings.Join(types, " or "))
		}
	}
	if enumValue, exists := schema["enum"]; exists {
		enum, ok := enumValue.([]any)
		if !ok || len(enum) == 0 {
			return fmt.Errorf("%s: enum must be a non-empty array", path)
		}
		matched := false
		for _, allowed := range enum {
			if jsonValuesEqual(value, allowed) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s: value is not in enum", path)
		}
	}
	if constValue, exists := schema["const"]; exists && !jsonValuesEqual(value, constValue) {
		return fmt.Errorf("%s: value does not match const", path)
	}

	if number, ok := value.(json.Number); ok {
		if minimum, exists := schema["minimum"]; exists {
			bound, err := schemaNumber(minimum)
			if err != nil {
				return fmt.Errorf("%s: invalid minimum: %w", path, err)
			}
			current, err := number.Float64()
			if err != nil || math.IsNaN(current) || math.IsInf(current, 0) || current < bound {
				return fmt.Errorf("%s: number is below minimum", path)
			}
		}
		if maximum, exists := schema["maximum"]; exists {
			bound, err := schemaNumber(maximum)
			if err != nil {
				return fmt.Errorf("%s: invalid maximum: %w", path, err)
			}
			current, err := number.Float64()
			if err != nil || math.IsNaN(current) || math.IsInf(current, 0) || current > bound {
				return fmt.Errorf("%s: number is above maximum", path)
			}
		}
	}

	if object, ok := value.(map[string]any); ok {
		if required, exists := schema["required"]; exists {
			fields, err := schemaStringArray(required)
			if err != nil {
				return fmt.Errorf("%s: invalid required: %w", path, err)
			}
			for _, field := range fields {
				if _, present := object[field]; !present {
					return fmt.Errorf("%s: missing required field %q", path, field)
				}
			}
		}
		properties, err := schemaProperties(schema["properties"])
		if err != nil {
			return fmt.Errorf("%s: invalid properties: %w", path, err)
		}
		additional, hasAdditional := schema["additionalProperties"]
		for key, item := range object {
			propertySchema, declared := properties[key]
			if declared {
				if err := validateSchemaValue(item, propertySchema, path+"."+key, false); err != nil {
					return err
				}
				continue
			}
			if !hasAdditional {
				continue
			}
			switch setting := additional.(type) {
			case bool:
				if !setting {
					return fmt.Errorf("%s: unknown field %q", path, key)
				}
			case map[string]any:
				if err := validateSchemaValue(item, setting, path+"."+key, false); err != nil {
					return err
				}
			default:
				return fmt.Errorf("%s: additionalProperties must be boolean or object", path)
			}
		}
	} else if _, hasRequired := schema["required"]; hasRequired {
		return fmt.Errorf("%s: required applies only to objects", path)
	}

	if itemsValue, exists := schema["items"]; exists {
		array, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%s: items applies only to arrays", path)
		}
		items, ok := itemsValue.(map[string]any)
		if !ok || items == nil {
			return fmt.Errorf("%s: items must be an object", path)
		}
		for index, item := range array {
			if err := validateSchemaValue(item, items, fmt.Sprintf("%s[%d]", path, index), false); err != nil {
				return err
			}
		}
	}
	if root {
		if _, exists := schema["additionalProperties"]; !exists {
			// Omitted additionalProperties intentionally retains JSON Schema's
			// permissive default; strict callers set it to false explicitly.
		}
	}
	return nil
}

func schemaTypes(value any) ([]string, error) {
	switch typed := value.(type) {
	case string:
		if typed == "" {
			return nil, errors.New("type must not be empty")
		}
		return []string{typed}, nil
	case []any:
		if len(typed) == 0 {
			return nil, errors.New("type array must not be empty")
		}
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			name, ok := item.(string)
			if !ok || name == "" {
				return nil, errors.New("type array must contain strings")
			}
			result = append(result, name)
		}
		return result, nil
	default:
		return nil, errors.New("type must be a string or array")
	}
}

func matchesJSONType(value any, typeName string) bool {
	switch typeName {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "number":
		_, ok := value.(json.Number)
		return ok
	case "integer":
		number, ok := value.(json.Number)
		return ok && isInteger(number)
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "null":
		return value == nil
	default:
		return false
	}
}

func isInteger(value json.Number) bool {
	parsed, err := strconv.ParseFloat(value.String(), 64)
	return err == nil && !math.IsNaN(parsed) && !math.IsInf(parsed, 0) && math.Trunc(parsed) == parsed
}

func schemaNumber(value any) (float64, error) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, errors.New("must be a number")
	}
	parsed, err := number.Float64()
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return 0, errors.New("must be a finite number")
	}
	return parsed, nil
}

func schemaStringArray(value any) ([]string, error) {
	items, ok := value.([]any)
	if !ok {
		return nil, errors.New("must be an array")
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok || text == "" {
			return nil, errors.New("must contain non-empty strings")
		}
		result = append(result, text)
	}
	return result, nil
}

func schemaProperties(value any) (map[string]map[string]any, error) {
	if value == nil {
		return map[string]map[string]any{}, nil
	}
	properties, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("must be an object")
	}
	result := make(map[string]map[string]any, len(properties))
	for key, item := range properties {
		propertySchema, ok := item.(map[string]any)
		if !ok || propertySchema == nil {
			return nil, fmt.Errorf("property %q must be an object", key)
		}
		result[key] = propertySchema
	}
	return result, nil
}

func jsonValuesEqual(left, right any) bool {
	leftNumber, leftIsNumber := left.(json.Number)
	rightNumber, rightIsNumber := right.(json.Number)
	if leftIsNumber || rightIsNumber {
		if !leftIsNumber || !rightIsNumber {
			return false
		}
		leftFloat, leftErr := leftNumber.Float64()
		rightFloat, rightErr := rightNumber.Float64()
		return leftErr == nil && rightErr == nil && leftFloat == rightFloat
	}
	leftObject, leftIsObject := left.(map[string]any)
	rightObject, rightIsObject := right.(map[string]any)
	if leftIsObject || rightIsObject {
		if !leftIsObject || !rightIsObject || len(leftObject) != len(rightObject) {
			return false
		}
		for key, leftValue := range leftObject {
			rightValue, ok := rightObject[key]
			if !ok || !jsonValuesEqual(leftValue, rightValue) {
				return false
			}
		}
		return true
	}
	leftArray, leftIsArray := left.([]any)
	rightArray, rightIsArray := right.([]any)
	if leftIsArray || rightIsArray {
		if !leftIsArray || !rightIsArray || len(leftArray) != len(rightArray) {
			return false
		}
		for index := range leftArray {
			if !jsonValuesEqual(leftArray[index], rightArray[index]) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(left, right)
}
