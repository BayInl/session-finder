package llm

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const (
	SegmentDecisionNew     = "new"
	SegmentDecisionSame    = "same"
	SegmentDecisionConfirm = "confirm"
	maxSegmentTurns        = 64
)

// SegmentTurn is one user-turn classification. Index is 0-based in the
// user-turn list sent to the model, not the full transcript.
type SegmentTurn struct {
	Index    int    `json:"index"`
	Decision string `json:"decision"`
}

// SegmentResult is the schema-validated output of intent segmentation.
type SegmentResult struct {
	Turns []SegmentTurn `json:"turns"`
}

var segmentSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["turns"],"properties":{"turns":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["index","decision"],"properties":{"index":{"type":"integer"},"decision":{"type":"string"}}}}}}`)

func SegmentSchema() json.RawMessage {
	return append(json.RawMessage(nil), segmentSchema...)
}

func DecodeSegments(data []byte) (SegmentResult, error) {
	if err := ValidateJSONSchema(data, segmentSchema); err != nil {
		return SegmentResult{}, fmt.Errorf("%w: %v", ErrSchemaViolation, err)
	}
	var result SegmentResult
	if err := json.Unmarshal(data, &result); err != nil {
		return SegmentResult{}, fmt.Errorf("%w: %v", ErrSchemaViolation, err)
	}
	if result.Turns == nil {
		result.Turns = []SegmentTurn{}
	}
	if len(result.Turns) > maxSegmentTurns {
		return SegmentResult{}, fmt.Errorf("%w: too many turns", ErrSchemaViolation)
	}
	seen := map[int]bool{}
	normalized := make([]SegmentTurn, 0, len(result.Turns))
	for _, turn := range result.Turns {
		decision := strings.ToLower(strings.TrimSpace(turn.Decision))
		switch decision {
		case SegmentDecisionNew, SegmentDecisionSame, SegmentDecisionConfirm:
		default:
			return SegmentResult{}, fmt.Errorf("%w: invalid decision %q", ErrSchemaViolation, turn.Decision)
		}
		if turn.Index < 0 || seen[turn.Index] {
			return SegmentResult{}, fmt.Errorf("%w: invalid turn index %d", ErrSchemaViolation, turn.Index)
		}
		seen[turn.Index] = true
		normalized = append(normalized, SegmentTurn{Index: turn.Index, Decision: decision})
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i].Index < normalized[j].Index })
	result.Turns = normalized
	return result, nil
}
