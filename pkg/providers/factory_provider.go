// Compa - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 Compa contributors

package providers

import (
	"fmt"
	"strings"

	"github.com/xibodev/compa/v4/pkg/config"
	"github.com/xibodev/compa/v4/pkg/providers/coretransport"
	"github.com/xibodev/compa/v4/pkg/providers/openai_compat"
)

// CallSpec is what the agent pipeline reads about one model call: one model
// on one provider instance, with the instance's runtime settings. It is
// never persisted and carries no credential.
type CallSpec struct {
	// DisplayName names the target, "instance-id/model-id".
	DisplayName string
	// ModelID is the model the request names.
	ModelID string
	// Streaming turns provider streaming on. It is on unless the instance
	// turns it off.
	Streaming bool
	// ThinkingLevel is the extended-thinking level; empty leaves it to the
	// agent.
	ThinkingLevel string
	// RPM caps requests per minute; 0 is unlimited.
	RPM int
	// MaxTokensField names the request's max-tokens field.
	MaxTokensField string
	// ToolSchemaTransform adapts tool schemas ("" or "simple").
	ToolSchemaTransform string
	// ExtraBody holds fields merged into every request body.
	ExtraBody map[string]any
}

// callSpecFromInstance returns the spec of modelID on instance.
func callSpecFromInstance(instance *config.ProviderInstanceConfig, modelID string) *CallSpec {
	spec := &CallSpec{DisplayName: instance.ID + "/" + modelID, ModelID: modelID, Streaming: true}
	if runtime := instance.Runtime; runtime != nil {
		spec.RPM = runtime.RPM
		if runtime.Streaming != nil {
			spec.Streaming = *runtime.Streaming
		}
		spec.ThinkingLevel = strings.TrimSpace(runtime.ThinkingLevel)
		spec.MaxTokensField = strings.TrimSpace(runtime.MaxTokensField)
		spec.ToolSchemaTransform = strings.TrimSpace(runtime.ToolSchemaTransform)
		// A copy: a provider may add fields of its own to the body it sends.
		spec.ExtraBody = cloneAnyMap(runtime.ExtraBody)
	}
	return spec
}

// CreateProviderFromInstance builds the chat provider of one model on one
// provider instance from that instance alone: Compa's OpenAI-compatible chat
// client, shaping requests with the instance's runtime settings, sending
// them through the instance's llmgw-core provider (see NewCoreProvider) with
// the credential secret - what the instance's auth_connection_ref resolved
// to - provides.
func CreateProviderFromInstance(
	instance *config.ProviderInstanceConfig,
	modelID string,
	secret string,
) (LLMProvider, error) {
	if instance == nil {
		return nil, fmt.Errorf("provider instance is required")
	}
	if strings.TrimSpace(modelID) == "" {
		return nil, fmt.Errorf("model is required")
	}
	credential, err := InstanceCredential(instance, secret)
	if err != nil {
		return nil, err
	}
	provider, httpClient, err := newCoreProvider(instance)
	if err != nil {
		return nil, err
	}
	spec := callSpecFromInstance(instance, modelID)
	name := strings.TrimSpace(instance.ProviderKind)
	if extensionProvider := instance.ExtensionProvider(); extensionProvider != "" {
		name = extensionProvider
	}
	name = NormalizeProvider(name)
	if name == "minimax" {
		// MiniMax returns reasoning separately only when asked.
		if spec.ExtraBody == nil {
			spec.ExtraBody = map[string]any{}
		}
		if _, ok := spec.ExtraBody["reasoning_split"]; !ok {
			spec.ExtraBody["reasoning_split"] = true
		}
	}
	options := []openai_compat.Option{
		openai_compat.WithProviderName(name),
		openai_compat.WithMaxTokensField(spec.MaxTokensField),
		openai_compat.WithExtraBody(spec.ExtraBody),
	}
	if extensionProvider := instance.ExtensionProvider(); extensionProvider != "" {
		// An extension daemon relays its upstream's stream, while its answer
		// to a request that does not stream can lack the tool calls the
		// stream carries. Every call to one streams, and a call that does not
		// stream is assembled from the stream.
		options = append(options, openai_compat.WithStreamedChat())
	}
	if runtimeType, _ := InstanceRuntimeType(instance); runtimeType == "anthropic" {
		// Anthropic caches prompt prefixes up to the system parts marked
		// for it; translation carries their cache_control.
		options = append(options, openai_compat.WithSystemParts())
	}
	transport := &coretransport.Transport{
		Provider:   provider,
		Credential: credential,
		Refresh:    InstanceCredentialRefresher(instance),
		// A stream may take as long to start as a response may.
		FirstFrameTimeout: instanceRequestTimeout(instance),
	}
	if httpClient != nil {
		transport.CloseIdle = httpClient.CloseIdleConnections
	}
	client := openai_compat.NewProvider(instance.Endpoint, coretransport.Client(transport), options...)
	return wrapProviderWithToolSchemaTransform(client, spec.ToolSchemaTransform)
}
