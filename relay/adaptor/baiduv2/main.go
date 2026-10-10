package baiduv2

import (
	"fmt"

	"github.com/sakura5215/InferGateway/relay/meta"
	"github.com/sakura5215/InferGateway/relay/relaymode"
)

func GetRequestURL(meta *meta.Meta) (string, error) {
	switch meta.Mode {
	case relaymode.ChatCompletions:
		return fmt.Sprintf("%s/v2/chat/completions", meta.BaseURL), nil
	default:
	}
	return "", fmt.Errorf("unsupported relay mode %d for baidu v2", meta.Mode)
}
