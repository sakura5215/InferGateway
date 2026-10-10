package minimax

import (
	"fmt"
	"github.com/sakura5215/InferGateway/relay/meta"
	"github.com/sakura5215/InferGateway/relay/relaymode"
)

func GetRequestURL(meta *meta.Meta) (string, error) {
	if meta.Mode == relaymode.ChatCompletions {
		return fmt.Sprintf("%s/v1/text/chatcompletion_v2", meta.BaseURL), nil
	}
	return "", fmt.Errorf("unsupported relay mode %d for minimax", meta.Mode)
}
