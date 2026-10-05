package provider

import "fmt"

type Wire string

const (
	OpenAIResponses   Wire = "openai-responses"
	AnthropicMessages Wire = "anthropic-messages"
	OpenAIChat        Wire = "openai-chat-completions"
)

type EvidenceClass string

const (
	DocumentedControl       EvidenceClass = "documented-control"
	CompatibilityCandidate  EvidenceClass = "compatibility-candidate"
	DocumentedConfiguration EvidenceClass = "documented-configuration"
)

type Preset struct {
	ID            string        `json:"id"`
	Wire          Wire          `json:"wire"`
	BaseURL       string        `json:"base_url"`
	Path          string        `json:"path"`
	Model         string        `json:"model"`
	EvidenceClass EvidenceClass `json:"evidence_class"`
	SourceURL     string        `json:"source_url"`
	Qualification string        `json:"qualification"`
}

var zaiChinaCodingPlan = []Preset{
	{
		ID: "zai-cn-responses-control", Wire: OpenAIResponses,
		BaseURL: "https://open.bigmodel.cn/api/v1", Path: "/responses", Model: "glm-5.3",
		EvidenceClass: DocumentedControl,
		SourceURL:     "https://docs.bigmodel.cn/cn/coding-plan/tool/codex",
		Qualification: "Codex configuration explicitly documents this base, Responses wire, and model; the composed /responses URL is inferred.",
	},
	{
		ID: "zai-cn-responses-glm-5.3-flash", Wire: OpenAIResponses,
		BaseURL: "https://open.bigmodel.cn/api/v1", Path: "/responses", Model: "glm-5.3-flash",
		EvidenceClass: CompatibilityCandidate,
		SourceURL:     "https://docs.bigmodel.cn/cn/coding-plan/latest-model.md",
		Qualification: "The model is documented for China Coding Plan, but its exact Codex Responses combination is not.",
	},
	{
		ID: "zai-cn-anthropic", Wire: AnthropicMessages,
		BaseURL: "https://open.bigmodel.cn/api/anthropic", Path: "/v1/messages", Model: "glm-5.3-flash[1m]",
		EvidenceClass: DocumentedConfiguration,
		SourceURL:     "https://docs.bigmodel.cn/cn/coding-plan/tool/claude",
		Qualification: "Claude Code configuration explicitly documents this base and model alias.",
	},
	{
		ID: "zai-cn-chat", Wire: OpenAIChat,
		BaseURL: "https://open.bigmodel.cn/api/coding/paas/v4", Path: "/chat/completions", Model: "glm-5.3-flash",
		EvidenceClass: CompatibilityCandidate,
		SourceURL:     "https://docs.bigmodel.cn/cn/coding-plan/quick-start.md",
		Qualification: "The Chat Completions base and model availability are documented separately. Observed 2026-10-04 with the owner's key, identically on this base and on https://api.z.ai/api/coding/paas/v4: the base lists this model; reasoning is on by default and returns as message.reasoning_content; thinking {type: enabled|disabled} is accepted (disabled stops reasoning on glm-5.3, while glm-4.5-air still reasons briefly); enable_thinking is refused with code 1210; reasoning_effort is accepted; an assistant turn replayed with its reasoning_content is accepted, which is what the vendor's Thinking Mode guide asks of the Coding Plan's preserved thinking (https://docs.z.ai/guides/capabilities/thinking-mode).",
	},
}

func ZAIChinaCodingPlan() []Preset {
	return append([]Preset(nil), zaiChinaCodingPlan...)
}

func ZAIPreset(id string) (Preset, error) {
	for _, preset := range zaiChinaCodingPlan {
		if preset.ID == id {
			return preset, nil
		}
	}
	return Preset{}, fmt.Errorf("provider: unknown Z.ai preset %q", id)
}
