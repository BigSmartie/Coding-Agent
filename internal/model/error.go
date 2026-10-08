package model

import (
	"context"
	"errors"

	"github.com/BigSmartie/Coding-Agent/internal/message"
)

type ErrorModel struct {
	Err error
}

func (m ErrorModel) Next(_ context.Context, _ []message.Message) (message.Step, error) {
	if m.Err == nil {
		return message.Step{}, errors.New("model is not configured")
	}
	return message.Step{}, m.Err
}
