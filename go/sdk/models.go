package sdk

import (
	"context"
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
	request := providerFrame("provider.models.list.request", map[string]any{})
	if req.ProviderID != "" {
		request.Payload = mustMarshal(map[string]any{"provider_id": req.ProviderID})
	}
	sub := s.transport.subscribeStream(string(request.ID))
	defer sub.close()
	response, err := providerRequest(ctx, s.transport, sub, s.timeout, request)
	if err != nil {
		return nil, err
	}
	if response.Type != "provider.models.list.response" {
		return nil, &ProtocolError{Code: CodeMalformedResponse, Message: "expected provider.models.list.response"}
	}
	result := &ListModelsResponse{Models: []ModelDescriptor{}, FetchedAt: time.Now()}
	for _, raw := range payloadObject(response.Payload).arr("models") {
		entry, ok := raw.(map[string]any)
		if !ok {
			return nil, &ProtocolError{Code: CodeMalformedResponse, Message: "model entry is not an object"}
		}
		model := jsonObject(entry)
		source, err := oapModelSource(model)
		if err != nil {
			return nil, err
		}
		lifecycle, err := oapModelLifecycle(model)
		if err != nil {
			return nil, err
		}
		auth, err := oapModelAuth(model)
		if err != nil {
			return nil, err
		}
		if req.API != "" && model.str("wire") != req.API {
			continue
		}
		if req.ModelID != "" && model.str("model_id") != req.ModelID {
			continue
		}
		if req.IncludeDeprecated != nil && !*req.IncludeDeprecated && lifecycle != nil && *lifecycle == LifecycleDeprecated {
			continue
		}
		if req.IncludeLoginRequired != nil && !*req.IncludeLoginRequired && auth == AuthLoginRequired {
			continue
		}
		descriptor := ModelDescriptor{ModelRef: model.str("model_ref"), ModelID: model.str("model_id"),
			DisplayName: model.str("display_name"), ProviderID: model.str("provider_id"), API: model.str("wire"),
			AuthStatus: auth, Lifecycle: lifecycle,
			Source: source, ContextWindow: model.intOr(0, "context_window"),
			MaxOutputTokens: model.intOr(0, "max_output_tokens"), ReasoningDefault: ReasoningLevel(model.str("reasoning_default"))}
		if descriptor.DisplayName == "" {
			descriptor.DisplayName = descriptor.ModelID
		}
		for _, item := range model.arr("capabilities") {
			if name, ok := item.(string); ok {
				descriptor.Capabilities = append(descriptor.Capabilities, ModelCapability(name))
			}
		}
		if published, ok := model["cost"].(map[string]any); ok {
			rates := jsonObject(published)
			descriptor.Cost = &ModelCost{}
			if value, ok := rates.num("input"); ok {
				descriptor.Cost.Input = value
			}
			if value, ok := rates.num("output"); ok {
				descriptor.Cost.Output = value
			}
			if value, ok := rates.num("cache_read"); ok {
				descriptor.Cost.CacheRead = value
			}
			if value, ok := rates.num("cache_write"); ok {
				descriptor.Cost.CacheWrite = value
			}
		}
		for _, item := range model.arr("input_modalities") {
			if name, ok := item.(string); ok {
				descriptor.InputModalities = append(descriptor.InputModalities, name)
			}
		}
		for _, item := range model.arr("output_modalities") {
			if name, ok := item.(string); ok {
				descriptor.OutputModalities = append(descriptor.OutputModalities, name)
			}
		}
		for _, item := range model.arr("reasoning_levels") {
			if name, ok := item.(string); ok {
				descriptor.ReasoningLevels = append(descriptor.ReasoningLevels, ReasoningLevel(name))
			}
		}
		descriptor.ReleaseDate = model.str("release_date")
		descriptor.Family = model.str("family")
		result.Models = append(result.Models, descriptor)
	}
	if published, ok := payloadObject(response.Payload)["catalog"].(map[string]any); ok {
		catalog := &ModelCatalog{}
		if value, ok := jsonObject(published).num("observed_at_ms"); ok {
			catalog.ObservedAtMS = int64(value)
		}
		if value, ok := published["complete"].(bool); ok {
			catalog.Complete = value
		}
		result.Catalog = catalog
	}
	return result, nil
}

func (s *ModelsService) Resolve(ctx context.Context, req ResolveModelRequest) (*ModelDescriptor, error) {
	response, err := s.List(ctx, ListModelsRequest{ProviderID: req.ProviderID, API: req.API, ModelID: req.ModelID})
	if err != nil {
		return nil, err
	}
	if len(response.Models) != 1 {
		return nil, &ProtocolError{Code: CodeInvalidRequest, Message: "model not found or ambiguous"}
	}
	return &response.Models[0], nil
}
