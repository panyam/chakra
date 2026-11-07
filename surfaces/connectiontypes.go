package surfaces

import (
	sdk "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
	agent "github.com/panyam/chakra"
	"github.com/panyam/chakra/host"
	"github.com/panyam/chakra/provider/anthropic"
)

// ConnectionTypes is how chakra's binaries build connections (#274). It
// builds "anthropic" through Anthropic's SDK (provider/anthropic), which
// asks for prompt caching on every request, and leaves every other Type to
// the host. Pass it with host.WithConnectionTypes, or call its Build where
// code would call host.DefaultProviderBuilder.
func ConnectionTypes() host.ConnectionTypes {
	return host.ConnectionTypes{"anthropic": AnthropicSDKProvider}
}

// AnthropicSDKProvider builds an "anthropic" connection on the SDK. The key
// and base URL arrive resolved. An empty key leaves the SDK to find one
// itself, from ANTHROPIC_API_KEY or an `ant auth login` profile.
// MaxTokens caps each reply, and ReasoningEffort goes out as
// output_config.effort.
func AnthropicSDKProvider(conn host.ConnectionConfig) (agent.Provider, error) {
	var opts []option.RequestOption
	if conn.APIKey != "" {
		opts = append(opts, option.WithAPIKey(conn.APIKey))
	}
	if conn.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(conn.BaseURL))
	}
	client := sdk.NewClient(opts...)
	return anthropic.New(&client, anthropic.Config{Model: conn.Model, MaxTokens: conn.MaxTokens, Effort: conn.ReasoningEffort})
}
