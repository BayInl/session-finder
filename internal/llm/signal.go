package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/BayInl/session-finder/internal/extract"
)

var signalSchema = json.RawMessage(`{
  "type":"object",
  "additionalProperties":false,
  "required":["intent_kind","confidence","success_evidence","one_off_risk","secret_risk","recommended_action"],
  "properties":{
    "intent_kind":{"type":"string","enum":["unknown","correction","acceptance","decision","question","implementation","workflow"]},
    "confidence":{"type":"number","minimum":0,"maximum":1},
    "success_evidence":{"type":"array","items":{"type":"string"}},
    "one_off_risk":{"type":"number","minimum":0,"maximum":1},
    "secret_risk":{"type":"number","minimum":0,"maximum":1},
    "recommended_action":{"type":"string","enum":["draft","review","suppress"]}
  }
}`)

// SignalSchema returns a copy of the strict schema sent to compatible APIs.
func SignalSchema() json.RawMessage { return append(json.RawMessage(nil), signalSchema...) }

func decodeSignal(data []byte, output *extract.SignalBundle) error {
	if err := validateSignalJSON(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("%w: %v", ErrSchemaViolation, err)
	}
	return nil
}

func validateSignalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&raw); err != nil || raw == nil {
		if err == nil {
			err = fmt.Errorf("expected object")
		}
		return fmt.Errorf("%w: %v", ErrSchemaViolation, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("%w: trailing JSON", ErrSchemaViolation)
		}
		return fmt.Errorf("%w: invalid trailing JSON: %v", ErrSchemaViolation, err)
	}
	required := []string{"intent_kind", "confidence", "success_evidence", "one_off_risk", "secret_risk", "recommended_action"}
	for _, key := range required {
		if _, ok := raw[key]; !ok {
			return fmt.Errorf("%w: missing %q", ErrSchemaViolation, key)
		}
	}
	for key := range raw {
		found := false
		for _, requiredKey := range required {
			if key == requiredKey {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%w: unknown field %q", ErrSchemaViolation, key)
		}
	}
	var intent string
	if err := json.Unmarshal(raw["intent_kind"], &intent); err != nil {
		return fmt.Errorf("%w: intent_kind must be a string", ErrSchemaViolation)
	}
	if !validIntent(intent) {
		return fmt.Errorf("%w: invalid intent_kind %q", ErrSchemaViolation, intent)
	}
	var evidence []string
	if err := json.Unmarshal(raw["success_evidence"], &evidence); err != nil || evidence == nil {
		return fmt.Errorf("%w: success_evidence must be an array of strings", ErrSchemaViolation)
	}
	for _, item := range evidence {
		if strings.TrimSpace(item) == "" {
			return fmt.Errorf("%w: success_evidence items must be non-empty strings", ErrSchemaViolation)
		}
	}
	var action string
	if err := json.Unmarshal(raw["recommended_action"], &action); err != nil {
		return fmt.Errorf("%w: recommended_action must be a string", ErrSchemaViolation)
	}
	if action != extract.ActionDraft && action != extract.ActionReview && action != extract.ActionSuppress {
		return fmt.Errorf("%w: invalid recommended_action %q", ErrSchemaViolation, action)
	}
	for _, key := range []string{"confidence", "one_off_risk", "secret_risk"} {
		var value float64
		if string(raw[key]) == "null" {
			return fmt.Errorf("%w: %s must be a number", ErrSchemaViolation, key)
		}
		if err := json.Unmarshal(raw[key], &value); err != nil {
			return fmt.Errorf("%w: %s must be a number", ErrSchemaViolation, key)
		}
		if value < 0 || value > 1 {
			return fmt.Errorf("%w: %s must be between 0 and 1", ErrSchemaViolation, key)
		}
	}
	var bundle extract.SignalBundle
	decoder = json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&bundle); err != nil {
		return fmt.Errorf("%w: %v", ErrSchemaViolation, err)
	}
	return nil
}

func validIntent(value string) bool {
	switch value {
	case extract.IntentUnknown, extract.IntentCorrection, extract.IntentAcceptance, extract.IntentDecision, extract.IntentQuestion, extract.IntentImplement, extract.IntentWorkflow:
		return true
	default:
		return false
	}
}
