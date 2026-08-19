package executor

import (
	"context"
	"errors"
	"net/http"
)

type codexTransportStatusErr struct {
	statusErr
	cause error
}

func (e codexTransportStatusErr) Unwrap() error { return e.cause }

func codexTransportError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return codexTransportStatusErr{
		statusErr: statusErr{code: http.StatusBadGateway, msg: err.Error()},
		cause:     err,
	}
}
