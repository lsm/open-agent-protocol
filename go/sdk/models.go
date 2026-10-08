package sdk

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type AuthStatus string

const (
	AuthAuthenticated   AuthStatus = "authenticated"
	AuthLoginRequired   AuthStatus = "login_required"
	AuthExpired         AuthStatus = "expired"
	AuthRefreshing      AuthStatus = "refreshing"
	AuthLoginInProgress AuthStatus = "login_in_progress"
	AuthFailed          AuthStatus = "failed"
	AuthUnknown         AuthStatus = "unknown"
)

type ModelLifecycle string

const (
	LifecycleStable     ModelLifecycle = "stable"
	LifecyclePreview    ModelLifecycle = "preview"
	LifecycleDeprecated ModelLifecycle = "deprecated"
)

type ModelCapability string

const (
	CapabilityChat        ModelCapability = "chat"
	CapabilityStreaming   ModelCapability = "streaming"
	CapabilityTools       ModelCapability = "tools"
	CapabilityVision      ModelCapability = "vision"
	CapabilityReasoning   ModelCapability = "reasoning"
	CapabilityPromptCache ModelCapability = "prompt_cache"
	CapabilityAudioInput  ModelCapability = "audio_input"
	CapabilityAudioOutput ModelCapability = "audio_output"
)

type ModelSource string

const (
	SourceDynamic        ModelSource = "dynamic"
	SourceStaticFallback ModelSource = "static_fallback"
)

type ModelDescriptor struct {
	ModelRef string

	ModelID string

	DisplayName string

	ProviderID string
	API        string

	BaseURL string

	AuthStatus AuthStatus

	Lifecycle *ModelLifecycle `json:",omitempty"`

	Capabilities []ModelCapability

	Source *ModelSource `json:",omitempty"`

	ContextWindow   int
	MaxOutputTokens int

	ReasoningDefault ReasoningLevel

	Cost *ModelCost

	InputModalities  []string
	OutputModalities []string

	ReasoningLevels []ReasoningLevel

	ReleaseDate string

	Family string

	Metadata map[string]string
}

type ModelCost struct {
	Input      float64
	Output     float64
	CacheRead  float64
	CacheWrite float64
}

type ModelCatalog struct {
	ObservedAtMS int64

	Complete bool
}

type ListModelsRequest struct {
	ProviderID string

	API string

	ModelID string

	IncludeDeprecated *bool

	IncludeLoginRequired *bool
}

type ListModelsResponse struct {
	Models []ModelDescriptor

	Catalog *ModelCatalog

	FetchedAt time.Time

	CacheMaxAge time.Duration
}

type ResolveModelRequest struct {
	ProviderID string

	API string

	ModelID string
}

func Bool(b bool) *bool { return &b }

type ModelsService struct {
	transport *transport
	timeout   time.Duration
}

func (s *ModelsService) List(ctx context.Context, req ListModelsRequest) (*ListModelsResponse, error) {
	if s.transport != nil && !s.transport.legacyWire {
		return s.oapList(ctx, req)
	}
	if len(req.ProviderID) > maxProviderIDLength {
		return nil, &ProtocolError{Code: CodeInvalidRequest, Message: "provider_id exceeds the maximum length of 256 characters"}
	}
	if len(req.ModelID) > maxModelIDLength {
		return nil, &ProtocolError{Code: CodeInvalidRequest, Message: "model_id exceeds the maximum length of 256 characters"}
	}
	return s.dispatch(ctx, req)
}

func (s *ModelsService) Resolve(ctx context.Context, req ResolveModelRequest) (*ModelDescriptor, error) {
	if s.transport != nil && !s.transport.legacyWire {
		response, err := s.oapList(ctx, ListModelsRequest{ProviderID: req.ProviderID, API: req.API, ModelID: req.ModelID})
		if err != nil {
			return nil, err
		}
		if len(response.Models) != 1 {
			return nil, &ProtocolError{Code: CodeInvalidRequest, Message: "model not found or ambiguous"}
		}
		return &response.Models[0], nil
	}
	switch {
	case req.ProviderID == "":
		return nil, &ProtocolError{Code: CodeInvalidRequest, Message: "resolve requires provider_id"}
	case len(req.ProviderID) > maxProviderIDLength:
		return nil, &ProtocolError{Code: CodeInvalidRequest, Message: "provider_id exceeds the maximum length of 256 characters"}
	case req.ModelID == "":
		return nil, &ProtocolError{Code: CodeInvalidRequest, Message: "resolve requires model_id"}
	case len(req.ModelID) > maxModelIDLength:
		return nil, &ProtocolError{Code: CodeInvalidRequest, Message: "model_id exceeds the maximum length of 256 characters"}
	}

	response, err := s.dispatch(ctx, ListModelsRequest{
		ProviderID: req.ProviderID,
		API:        req.API,
		ModelID:    req.ModelID,
	})
	if err != nil {
		return nil, err
	}
	switch {
	case len(response.Models) == 0:
		return nil, &ProtocolError{Code: CodeInvalidRequest, Message: "model not found"}
	case len(response.Models) > 1:
		return nil, &ProtocolError{
			Code:    CodeInvalidRequest,
			Message: fmt.Sprintf("resolve matched %d models; expected exactly 1", len(response.Models)),
		}
	}

	model := response.Models[0]
	switch {
	case model.ProviderID != req.ProviderID:
		return nil, &ProtocolError{Code: CodeInvalidRequest, Message: "resolved model provider_id mismatch"}
	case model.ModelID != req.ModelID:
		return nil, &ProtocolError{Code: CodeInvalidRequest, Message: "resolved model_id mismatch"}
	case req.API != "" && model.API != req.API:
		return nil, &ProtocolError{Code: CodeInvalidRequest, Message: "resolved model api mismatch"}
	}
	return &model, nil
}

func (s *ModelsService) dispatch(ctx context.Context, req ListModelsRequest) (*ListModelsResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, &ProtocolError{Code: CodeInvalidRequest, Message: "models request aborted before start", err: err}
	}

	streamID := newULID()
	sub := s.transport.subscribeStream(streamID)
	defer sub.close()

	envelope := newStreamEnvelope("models_request", streamID, modelsRequestPayload(req))
	if err := s.transport.send(envelope); err != nil {
		return nil, protocolErrorFrom(err, streamID)
	}

	for {
		f, err := sub.nextFrame(ctx, s.timeout, "models_response")
		if err != nil {
			if isAbort(err) {
				cancelStream(s.transport, streamID)
			}
			return nil, protocolErrorFrom(err, streamID)
		}
		switch f.Type {
		case "ack":
			continue
		case "nack":
			payload := f.payload()
			return nil, &ProtocolError{
				Code:     payload.str("error_code", "code"),
				Message:  payload.strOrDefault("models request rejected", "reason", "message"),
				StreamID: streamID,
			}
		case "models_response":
			return parseModelsResponse(f, streamID)
		default:
			return nil, &ProtocolError{
				Code:     CodeMalformedResponse,
				Message:  fmt.Sprintf("unexpected frame type %q while awaiting models_response", f.Type),
				StreamID: streamID,
			}
		}
	}
}

func modelsRequestPayload(req ListModelsRequest) map[string]any {
	payload := map[string]any{}
	if req.ProviderID != "" {
		payload["provider_id"] = req.ProviderID
	}
	if req.API != "" {
		payload["api"] = req.API
	}
	if req.ModelID != "" {
		payload["model_id"] = req.ModelID
	}
	if req.IncludeDeprecated != nil {
		payload["include_deprecated"] = *req.IncludeDeprecated
	}
	if req.IncludeLoginRequired != nil {
		payload["include_login_required"] = *req.IncludeLoginRequired
	}
	return payload
}

const defaultCacheMaxAge = 5 * time.Minute

type wireModelsResponse struct {
	Models        []wireModelDescriptor `json:"models"`
	FetchedAtMs   *float64              `json:"fetched_at_ms"`
	CacheMaxAgeMs *float64              `json:"cache_max_age_ms"`
	Catalog       *wireModelCatalog     `json:"catalog"`
}

type wireModelDescriptor struct {
	ModelRef         string            `json:"model_ref"`
	ModelID          string            `json:"model_id"`
	DisplayName      string            `json:"display_name"`
	ProviderID       string            `json:"provider_id"`
	API              string            `json:"api"`
	BaseURL          string            `json:"base_url"`
	AuthStatus       string            `json:"auth_status"`
	Lifecycle        json.RawMessage   `json:"lifecycle"`
	Capabilities     *[]string         `json:"capabilities"`
	Source           json.RawMessage   `json:"source"`
	ContextWindow    *float64          `json:"context_window"`
	MaxOutputTokens  *float64          `json:"max_output_tokens"`
	ReasoningDefault *string           `json:"reasoning_default"`
	Cost             *wireModelCost    `json:"cost"`
	InputModalities  *[]string         `json:"input_modalities"`
	OutputModalities *[]string         `json:"output_modalities"`
	ReasoningLevels  *[]string         `json:"reasoning_levels"`
	ReleaseDate      *string           `json:"release_date"`
	Family           *string           `json:"family"`
	Metadata         map[string]string `json:"metadata"`
}

type wireModelCost struct {
	Input      *float64 `json:"input"`
	Output     *float64 `json:"output"`
	CacheRead  *float64 `json:"cache_read"`
	CacheWrite *float64 `json:"cache_write"`
}

type wireModelCatalog struct {
	ObservedAtMS *float64 `json:"observed_at_ms"`
	Complete     *bool    `json:"complete"`
}

func (w *wireModelCost) descriptor() *ModelCost {
	if w == nil {
		return nil
	}
	cost := &ModelCost{}
	if w.Input != nil {
		cost.Input = *w.Input
	}
	if w.Output != nil {
		cost.Output = *w.Output
	}
	if w.CacheRead != nil {
		cost.CacheRead = *w.CacheRead
	}
	if w.CacheWrite != nil {
		cost.CacheWrite = *w.CacheWrite
	}
	return cost
}

func modelLifecycle(raw json.RawMessage) (*ModelLifecycle, string, bool) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil, "", true
	}
	if trimmed == "null" {
		return nil, "lifecycle must be a string when present, not null", false
	}
	var name string
	if err := json.Unmarshal(raw, &name); err != nil || name == "" {
		return nil, "lifecycle must be a non-empty string when present", false
	}
	switch ModelLifecycle(name) {
	case LifecycleStable, LifecyclePreview, LifecycleDeprecated:
		mapped := ModelLifecycle(name)
		return &mapped, "", true
	default:
		return nil, "lifecycle has unknown value: " + name, false
	}
}

func modelSource(raw json.RawMessage) (*ModelSource, string, bool) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil, "", true
	}
	if trimmed == "null" {
		return nil, "source must be a string when present, not null", false
	}
	var name string
	if err := json.Unmarshal(raw, &name); err != nil || name == "" {
		return nil, "source must be a non-empty string when present", false
	}
	var mapped ModelSource
	switch ModelSource(name) {
	case SourceDynamic, SourceStaticFallback:
		mapped = ModelSource(name)
	default:
		return nil, "source has unknown value: " + name, false
	}
	return &mapped, "", true
}

func text(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func reasoningLevelsOf(values *[]string) []ReasoningLevel {
	if values == nil {
		return nil
	}
	levels := make([]ReasoningLevel, 0, len(*values))
	for _, value := range *values {
		levels = append(levels, ReasoningLevel(value))
	}
	return levels
}

func stringsOf(values *[]string) []string {
	if values == nil {
		return nil
	}
	return *values
}

func parseModelsResponse(f *frame, streamID string) (*ListModelsResponse, error) {
	malformed := func(format string, args ...any) error {
		return &ProtocolError{Code: CodeMalformedResponse, Message: fmt.Sprintf(format, args...), StreamID: streamID}
	}
	if len(f.Payload) == 0 {
		return nil, malformed("models_response is missing its payload object")
	}
	var wire wireModelsResponse
	if err := json.Unmarshal(f.Payload, &wire); err != nil {
		return nil, malformed("models_response payload did not decode: %v", err)
	}
	if wire.Models == nil {
		return nil, malformed("models_response is missing its 'models' array")
	}
	if wire.FetchedAtMs == nil {
		return nil, malformed("models_response is missing a numeric 'fetched_at_ms'")
	}

	var catalog *ModelCatalog
	if wire.Catalog != nil {
		catalog = &ModelCatalog{}
		if wire.Catalog.ObservedAtMS != nil {
			catalog.ObservedAtMS = int64(*wire.Catalog.ObservedAtMS)
		}
		if wire.Catalog.Complete != nil {
			catalog.Complete = *wire.Catalog.Complete
		}
	}

	cacheMaxAge := defaultCacheMaxAge
	if wire.CacheMaxAgeMs != nil {
		cacheMaxAge = time.Duration(*wire.CacheMaxAgeMs) * time.Millisecond
	}

	models := make([]ModelDescriptor, 0, len(wire.Models))
	for i, raw := range wire.Models {
		model, err := parseModelDescriptor(raw, i, streamID)
		if err != nil {
			return nil, err
		}
		models = append(models, model)
	}
	return &ListModelsResponse{
		Models:      models,
		Catalog:     catalog,
		FetchedAt:   time.UnixMilli(int64(*wire.FetchedAtMs)),
		CacheMaxAge: cacheMaxAge,
	}, nil
}

func parseModelDescriptor(raw wireModelDescriptor, index int, streamID string) (ModelDescriptor, error) {
	malformed := func(format string, args ...any) error {
		return &ProtocolError{Code: CodeMalformedResponse, Message: fmt.Sprintf(format, args...), StreamID: streamID}
	}
	for name, value := range map[string]string{
		"model_ref":    raw.ModelRef,
		"model_id":     raw.ModelID,
		"display_name": raw.DisplayName,
		"provider_id":  raw.ProviderID,
		"api":          raw.API,
	} {
		if value == "" {
			return ModelDescriptor{}, malformed("models[%d].%s must be a non-empty string", index, name)
		}
	}
	if !knownAuthStatuses[AuthStatus(raw.AuthStatus)] {
		return ModelDescriptor{}, malformed("models[%d].auth_status has unknown value: %q", index, raw.AuthStatus)
	}
	lifecycle, lifecycleMessage, lifecycleOK := modelLifecycle(raw.Lifecycle)
	if !lifecycleOK {
		return ModelDescriptor{}, malformed("models[%d].%s", index, lifecycleMessage)
	}
	source, message, ok := modelSource(raw.Source)
	if !ok {
		return ModelDescriptor{}, malformed("models[%d].%s", index, message)
	}
	if raw.Capabilities == nil {
		return ModelDescriptor{}, malformed("models[%d].capabilities must be an array", index)
	}

	capabilities := make([]ModelCapability, 0, len(*raw.Capabilities))
	for i, capability := range *raw.Capabilities {
		if !knownCapabilities[ModelCapability(capability)] {
			return ModelDescriptor{}, malformed("models[%d].capabilities[%d] has unknown value: %q", index, i, capability)
		}
		capabilities = append(capabilities, ModelCapability(capability))
	}

	model := ModelDescriptor{
		ModelRef:         raw.ModelRef,
		ModelID:          raw.ModelID,
		DisplayName:      raw.DisplayName,
		ProviderID:       raw.ProviderID,
		API:              raw.API,
		BaseURL:          raw.BaseURL,
		AuthStatus:       AuthStatus(raw.AuthStatus),
		Lifecycle:        lifecycle,
		Capabilities:     capabilities,
		Source:           source,
		Metadata:         raw.Metadata,
		Cost:             raw.Cost.descriptor(),
		InputModalities:  stringsOf(raw.InputModalities),
		OutputModalities: stringsOf(raw.OutputModalities),
		ReasoningLevels:  reasoningLevelsOf(raw.ReasoningLevels),
		ReleaseDate:      text(raw.ReleaseDate),
		Family:           text(raw.Family),
	}
	if raw.ContextWindow != nil {
		model.ContextWindow = int(*raw.ContextWindow)
	}
	if raw.MaxOutputTokens != nil {
		model.MaxOutputTokens = int(*raw.MaxOutputTokens)
	}
	if raw.ReasoningDefault != nil {
		if !knownReasoningLevels[ReasoningLevel(*raw.ReasoningDefault)] {
			return ModelDescriptor{}, malformed("models[%d].reasoning_default has unknown value: %q", index, *raw.ReasoningDefault)
		}
		model.ReasoningDefault = ReasoningLevel(*raw.ReasoningDefault)
	}
	return model, nil
}

var (
	knownAuthStatuses = map[AuthStatus]bool{
		AuthAuthenticated: true, AuthLoginRequired: true, AuthExpired: true,
		AuthRefreshing: true, AuthLoginInProgress: true, AuthFailed: true, AuthUnknown: true,
	}
	knownSources = map[ModelSource]bool{
		SourceDynamic: true, SourceStaticFallback: true,
	}
	knownCapabilities = map[ModelCapability]bool{
		CapabilityChat: true, CapabilityStreaming: true, CapabilityTools: true,
		CapabilityVision: true, CapabilityReasoning: true, CapabilityPromptCache: true,
		CapabilityAudioInput: true, CapabilityAudioOutput: true,
	}
	knownReasoningLevels = map[ReasoningLevel]bool{
		ReasoningOff: true, ReasoningMinimal: true, ReasoningLow: true,
		ReasoningMedium: true, ReasoningHigh: true, ReasoningXHigh: true,
		ReasoningMax: true,
	}
)

func protocolErrorFrom(err error, streamID string) error {
	var streamErr *StreamError
	if asStreamError(err, &streamErr) {
		return &ProtocolError{Message: streamErr.Message, StreamID: streamID, err: streamErr.err}
	}
	return &ProtocolError{Message: err.Error(), StreamID: streamID, err: err}
}
