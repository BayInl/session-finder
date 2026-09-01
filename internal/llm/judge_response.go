package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	JudgeDispositionDraft    = "draft"
	JudgeDispositionReview   = "review"
	JudgeDispositionSuppress = "suppress"
)

// JudgeResponse is the shared model-controlled verdict used by candidate judges.
type JudgeResponse struct {
	Disposition string   `json:"disposition"`
	Confidence  float64  `json:"confidence"`
	ReasonCodes []string `json:"reason_codes"`
}

// RiskJudgeResponse extends JudgeResponse for pipelines whose admission policy
// consumes one-off and secret risk scores.
type RiskJudgeResponse struct {
	Disposition string   `json:"disposition"`
	Confidence  float64  `json:"confidence"`
	ReasonCodes []string `json:"reason_codes"`
	OneOffRisk  float64  `json:"one_off_risk"`
	SecretRisk  float64  `json:"secret_risk"`
}

var judgeResponseSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["disposition","confidence","reason_codes"],"properties":{"disposition":{"type":"string","enum":["draft","review","suppress"]},"confidence":{"type":"number","minimum":0,"maximum":1},"reason_codes":{"type":"array","items":{"type":"string"}}}}`)
var riskJudgeResponseSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["disposition","confidence","reason_codes","one_off_risk","secret_risk"],"properties":{"disposition":{"type":"string","enum":["draft","review","suppress"]},"confidence":{"type":"number","minimum":0,"maximum":1},"reason_codes":{"type":"array","items":{"type":"string"}},"one_off_risk":{"type":"number","minimum":0,"maximum":1},"secret_risk":{"type":"number","minimum":0,"maximum":1}}}`)

func JudgeResponseSchema() json.RawMessage {
	return append(json.RawMessage(nil), judgeResponseSchema...)
}

func RiskJudgeResponseSchema() json.RawMessage {
	return append(json.RawMessage(nil), riskJudgeResponseSchema...)
}

func DecodeJudgeResponse(data []byte) (JudgeResponse, error) {
	var response JudgeResponse
	if err := decodeJudgeResponse(data, judgeResponseSchema, &response); err != nil {
		return JudgeResponse{}, err
	}
	if err := normalizeJudgeResponse(&response.Disposition, response.Confidence, response.ReasonCodes); err != nil {
		return JudgeResponse{}, err
	}
	return response, nil
}

func DecodeRiskJudgeResponse(data []byte) (RiskJudgeResponse, error) {
	var response RiskJudgeResponse
	if err := decodeJudgeResponse(data, riskJudgeResponseSchema, &response); err != nil {
		return RiskJudgeResponse{}, err
	}
	if err := normalizeJudgeResponse(&response.Disposition, response.Confidence, response.ReasonCodes); err != nil {
		return RiskJudgeResponse{}, err
	}
	if response.OneOffRisk < 0 || response.OneOffRisk > 1 || response.SecretRisk < 0 || response.SecretRisk > 1 {
		return RiskJudgeResponse{}, errors.New("judge response risk values must be between 0 and 1")
	}
	return response, nil
}

func decodeJudgeResponse(data, schema json.RawMessage, output any) error {
	if err := ValidateJSONSchema(data, schema); err != nil {
		return fmt.Errorf("judge response schema violation: %w", err)
	}
	if err := json.Unmarshal(data, output); err != nil {
		return fmt.Errorf("judge response schema violation: %v", err)
	}
	return nil
}

func normalizeJudgeResponse(disposition *string, confidence float64, reasonCodes []string) error {
	*disposition = strings.ToLower(strings.TrimSpace(*disposition))
	if *disposition != JudgeDispositionDraft && *disposition != JudgeDispositionReview && *disposition != JudgeDispositionSuppress {
		return fmt.Errorf("judge response schema violation: invalid disposition %q", *disposition)
	}
	if confidence < 0 || confidence > 1 {
		return errors.New("judge response confidence must be between 0 and 1")
	}
	if reasonCodes == nil {
		return errors.New("judge response reason_codes must be an array")
	}
	if len(reasonCodes) > 32 {
		return errors.New("judge response has too many reason_codes")
	}
	for index, reason := range reasonCodes {
		reasonCodes[index] = strings.TrimSpace(reason)
		if reasonCodes[index] == "" || len([]rune(reasonCodes[index])) > 80 {
			return errors.New("judge response has an invalid reason_codes item")
		}
	}
	return nil
}
