package aiproxy

import "github.com/sakura5215/InferGateway/relay/adaptor/openai"

var ModelList = []string{""}

func init() {
	ModelList = openai.ModelList
}
