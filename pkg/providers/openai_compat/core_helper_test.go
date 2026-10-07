package openai_compat

import (
	core "github.com/xibodev/llmgw-core"
	coreproviders "github.com/xibodev/llmgw-core/providers"

	"github.com/xibodev/compa/v3/pkg/providers/coretransport"
)

// newTestProvider returns the chat client of an OpenAI-compatible upstream
// at endpoint, reached as production reaches it: through llmgw-core's
// OpenAI-compatible provider, authenticating with key.
func newTestProvider(key, endpoint string, opts ...Option) *Provider {
	provider, err := coreproviders.NewOpenAICompatible(coreproviders.OpenAICompatibleConfig{BaseURL: endpoint, ForwardAllFields: true})
	if err != nil {
		panic(err)
	}
	var credential *core.Credential
	if key != "" {
		credential = &core.Credential{APIKey: key}
	}
	transport := &coretransport.Transport{Provider: provider, Credential: coretransport.StaticCredential(credential)}
	return NewProvider(endpoint, coretransport.Client(transport), opts...)
}
