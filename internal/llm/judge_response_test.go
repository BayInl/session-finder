package llm

import (
	"strings"
	"testing"
)

func TestDecodeJudgeResponseStrictValidation(t *testing.T) {
	tooManyReasons := make([]string, 33)
	for index := range tooManyReasons {
		tooManyReasons[index] = `"reason"`
	}
	cases := []struct {
		name string
		data string
	}{
		{name: "missing field", data: `{"disposition":"draft","confidence":0.8}`},
		{name: "unknown field", data: `{"disposition":"draft","confidence":0.8,"reason_codes":["clear"],"extra":true}`},
		{name: "out of range", data: `{"disposition":"draft","confidence":1.1,"reason_codes":["clear"]}`},
		{name: "null reasons", data: `{"disposition":"draft","confidence":0.8,"reason_codes":null}`},
		{name: "too many reasons", data: `{"disposition":"draft","confidence":0.8,"reason_codes":[` + strings.Join(tooManyReasons, ",") + `]}`},
		{name: "empty reason", data: `{"disposition":"draft","confidence":0.8,"reason_codes":[" "]}`},
		{name: "trailing json", data: `{"disposition":"draft","confidence":0.8,"reason_codes":["clear"]} {}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := DecodeJudgeResponse([]byte(testCase.data)); err == nil {
				t.Fatalf("malformed response accepted: %s", testCase.data)
			}
		})
	}
}

func TestDecodeRiskJudgeResponseNormalizesValues(t *testing.T) {
	response, err := DecodeRiskJudgeResponse([]byte(`{"disposition":"review","confidence":0.8,"reason_codes":[" clear "],"one_off_risk":0.2,"secret_risk":0.1}`))
	if err != nil {
		t.Fatal(err)
	}
	if response.Disposition != JudgeDispositionReview || len(response.ReasonCodes) != 1 || response.ReasonCodes[0] != "clear" {
		t.Fatalf("response = %+v", response)
	}
	if _, err := DecodeRiskJudgeResponse([]byte(`{"disposition":"draft","confidence":0.8,"reason_codes":["clear"],"one_off_risk":-0.1,"secret_risk":0.1}`)); err == nil {
		t.Fatal("out-of-range risk accepted")
	}
}
