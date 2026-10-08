package model

import (
	"strings"

	"github.com/BigSmartie/Coding-Agent/internal/config"
)

// Capabilities describes what this adapter can encode and decode. It does not
// assert that an arbitrary model or compatible gateway accepts every feature.
// An unknown context window is zero until the user configures it explicitly.
type Capabilities struct {
	SchemaVersion           int    `json:"schemaVersion"`
	Provider                string `json:"provider"`
	Model                   string `json:"model"`
	WireAPI                 string `json:"wireApi"`
	ContextWindowTokens     int    `json:"contextWindowTokens,omitempty"`
	ContextWindowSource     string `json:"contextWindowSource"`
	MaxOutputTokens         int    `json:"maxOutputTokens,omitempty"`
	AdapterSupportsTools    bool   `json:"adapterSupportsTools"`
	AdapterParsesUsage      bool   `json:"adapterParsesUsage"`
	AdapterParsesCacheUsage bool   `json:"adapterParsesCacheUsage"`
	ReasoningControl        string `json:"reasoningControl,omitempty"`
	OpaqueReasoningState    bool   `json:"opaqueReasoningState"`
}

func CapabilitiesFor(runtime config.Runtime) Capabilities {
	provider := strings.ToLower(strings.TrimSpace(runtime.Provider))
	protocol := "anthropic_messages"
	if provider == "openai" || provider == "openai-compatible" {
		protocol = "openai_chat_completions"
		if normalizeOpenAIWireAPI(runtime.WireAPI) == "responses" {
			protocol = "openai_responses"
		}
	}
	if provider == "mock" {
		protocol = "mock"
	}
	capability := Capabilities{
		SchemaVersion:           1,
		Provider:                provider,
		Model:                   runtime.Model,
		WireAPI:                 protocol,
		ContextWindowTokens:     runtime.ContextWindowTokens,
		ContextWindowSource:     "unknown",
		MaxOutputTokens:         runtime.MaxOutputTokens,
		AdapterSupportsTools:    true,
		AdapterParsesUsage:      true,
		AdapterParsesCacheUsage: true,
		OpaqueReasoningState:    protocol == "anthropic_messages" || protocol == "openai_responses",
	}
	if runtime.ContextWindowTokens > 0 {
		capability.ContextWindowSource = "user_config"
	}
	if protocol == "openai_responses" {
		capability.ReasoningControl = "reasoning_effort"
	}
	if protocol == "mock" {
		capability.AdapterSupportsTools = false
		capability.AdapterParsesUsage = false
		capability.AdapterParsesCacheUsage = false
	}
	return capability
}
