package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const maxEvidenceResponse = 1 << 20

type EvidenceResult struct {
	PresetID   string   `json:"preset_id"`
	Wire       Wire     `json:"wire"`
	Model      string   `json:"model"`
	StatusCode int      `json:"status_code"`
	MediaType  string   `json:"media_type"`
	Events     []string `json:"events,omitempty"`
	Completed  bool     `json:"completed"`
}

func RunEvidence(ctx context.Context, client *http.Client, preset Preset, credential string) (EvidenceResult, error) {
	result := EvidenceResult{PresetID: preset.ID, Wire: preset.Wire, Model: preset.Model}
	if err := validatePreset(preset); err != nil {
		return result, err
	}
	if credential == "" {
		return result, errors.New("provider: an explicit credential is required")
	}
	if client == nil {
		client = http.DefaultClient
	}
	evidenceClient := *client
	evidenceClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	body, err := evidenceRequestBody(preset)
	if err != nil {
		return result, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(preset.BaseURL, "/")+preset.Path, bytes.NewReader(body))
	if err != nil {
		return result, err
	}
	request.Header.Set("Content-Type", "application/json")
	switch preset.Wire {
	case AnthropicMessages:
		request.Header.Set("x-api-key", credential)
		request.Header.Set("anthropic-version", "2023-06-01")
	default:
		request.Header.Set("Authorization", "Bearer "+credential)
	}
	response, err := evidenceClient.Do(request)
	if err != nil {
		return result, fmt.Errorf("provider: evidence request failed: %w", err)
	}
	defer response.Body.Close()
	result.StatusCode = response.StatusCode
	result.MediaType = strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0])
	limited := io.LimitReader(response.Body, maxEvidenceResponse+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return result, fmt.Errorf("provider: read evidence response: %w", err)
	}
	if len(data) > maxEvidenceResponse {
		return result, errors.New("provider: evidence response exceeded 1 MiB")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return result, fmt.Errorf("provider: evidence endpoint returned HTTP %d", response.StatusCode)
	}
	if result.MediaType != "text/event-stream" {
		return result, fmt.Errorf("provider: expected text/event-stream, got %q", result.MediaType)
	}
	result.Events, result.Completed, err = inspectSSE(preset.Wire, data)
	if err != nil {
		return result, err
	}
	if !result.Completed {
		return result, errors.New("provider: stream ended without a completion marker")
	}
	return result, nil
}

func validatePreset(preset Preset) error {
	base, err := url.Parse(preset.BaseURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return errors.New("provider: preset has an invalid base URL")
	}
	if preset.ID == "" || preset.Model == "" || !strings.HasPrefix(preset.Path, "/") || strings.ContainsAny(preset.Path, "?#") {
		return errors.New("provider: preset is incomplete")
	}
	switch preset.Wire {
	case OpenAIResponses, AnthropicMessages, OpenAIChat:
		return nil
	default:
		return fmt.Errorf("provider: unsupported wire %q", preset.Wire)
	}
}

func evidenceRequestBody(preset Preset) ([]byte, error) {
	var request any
	switch preset.Wire {
	case OpenAIResponses:
		request = struct {
			Model  string `json:"model"`
			Input  string `json:"input"`
			Stream bool   `json:"stream"`
		}{preset.Model, "Reply with OK.", true}
	case AnthropicMessages:
		request = struct {
			Model     string `json:"model"`
			MaxTokens int    `json:"max_tokens"`
			Messages  []any  `json:"messages"`
			Stream    bool   `json:"stream"`
		}{preset.Model, 8, []any{map[string]string{"role": "user", "content": "Reply with OK."}}, true}
	case OpenAIChat:
		request = struct {
			Model     string `json:"model"`
			Messages  []any  `json:"messages"`
			MaxTokens int    `json:"max_tokens"`
			Stream    bool   `json:"stream"`
		}{preset.Model, []any{map[string]string{"role": "user", "content": "Reply with OK."}}, 8, true}
	default:
		return nil, fmt.Errorf("provider: unsupported wire %q", preset.Wire)
	}
	return json.Marshal(request)
}

func inspectSSE(wire Wire, data []byte) ([]string, bool, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), maxEvidenceResponse)
	var events []string
	var event string
	completed := false
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event: "))
			if event != "" {
				events = append(events, event)
			}
		case strings.HasPrefix(line, "data: "):
			dataValue := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
			if dataValue == "[DONE]" {
				completed = wire == OpenAIChat
				continue
			}
			var value struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal([]byte(dataValue), &value); err != nil {
				return events, false, errors.New("provider: malformed SSE JSON")
			}
			if event == "" && value.Type != "" {
				events = append(events, value.Type)
			}
			if (wire == OpenAIResponses && value.Type == "response.completed") || (wire == AnthropicMessages && value.Type == "message_stop") {
				completed = true
			}
		case line == "":
			event = ""
		}
	}
	if err := scanner.Err(); err != nil {
		return events, false, fmt.Errorf("provider: read SSE: %w", err)
	}
	return events, completed, nil
}
