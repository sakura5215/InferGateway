package openai

import (
	"context"
	"fmt"

	"github.com/sakura5215/InferGateway/common/logger"
	"github.com/sakura5215/InferGateway/relay/model"
)

func ErrorWrapper(err error, code string, statusCode int) *model.ErrorWithStatusCode {
	logger.Error(context.TODO(), fmt.Sprintf("[%s]%+v", code, err))

	Error := model.Error{
		Message: err.Error(),
		Type:    "one_api_error",
		Code:    code,
	}
	return &model.ErrorWithStatusCode{
		Error:      Error,
		StatusCode: statusCode,
	}
}
