package llm

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/BayInl/session-finder/internal/extract"
	"github.com/BayInl/session-finder/internal/record"
)

// OfflineClient performs pure local rule analysis. It cannot make network
// calls and is the default provider.
type OfflineClient struct{}

func (OfflineClient) Offline() bool { return true }

// NewOffline returns an offline-only client.
func NewOffline() Client { return &OfflineClient{} }

func (c *OfflineClient) Complete(_ context.Context, request CompletionRequest) (CompletionResponse, error) {
	if err := requireSignalSchema(request.Schema); err != nil {
		return CompletionResponse{}, err
	}
	messages := make([]record.MessageRecord, 0, len(request.Transcript))
	for _, message := range request.Transcript {
		messages = append(messages, record.MessageRecord{Role: message.Role, Text: message.Content})
	}
	bundle := extract.Analyze(messages)
	data, err := json.Marshal(bundle)
	if err != nil {
		return CompletionResponse{}, err
	}
	var validated extract.SignalBundle
	if err := decodeSignal(data, &validated); err != nil {
		return CompletionResponse{}, err
	}
	return CompletionResponse{Provider: ProviderOffline, JSON: data}, nil
}

func requireSignalSchema(schema json.RawMessage) error {
	if len(bytes.TrimSpace(schema)) == 0 {
		return nil
	}
	if bytes.Equal(bytes.TrimSpace(schema), bytes.TrimSpace(signalSchema)) {
		return nil
	}
	return ErrOfflineUnsupported
}
