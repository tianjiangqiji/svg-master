package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type noRetryError struct{ error }

func (e *noRetryError) Unwrap() error { return e.error }

// Retryable 区分流中途断连与确定性错误/已耗尽的 HTTP 重试。
func Retryable(err error) bool {
	var stop *noRetryError
	return !errors.As(err, &stop)
}

func noRetry(err error) error { return &noRetryError{err} }

// NoRetry prevents replaying a stateful request whose execution is owned by another client.
func NoRetry(err error) error { return noRetry(err) }

func errorEvents(err error) []StreamEvent {
	return []StreamEvent{{Kind: EvError, Err: err.Error(), Cause: err}}
}

func remoteError(protocol, code, message string) error {
	err := fmt.Errorf("%s 错误：%s", protocol, truncateRunes(message, errCharLimit))
	switch strings.ToLower(code) {
	case "overloaded_error", "api_error", "server_error", "rate_limit_error", "rate_limit_exceeded", "internal", "unavailable", "resource_exhausted", "408", "429", "500", "502", "503", "504":
		return err
	}
	return noRetry(err)
}

func stopReasonError(protocol, reason string) error {
	return noRetry(fmt.Errorf("%s 响应未完成或被拒绝：%s；请检查输出上限及渠道限制", protocol, reason))
}

func envelopeError(raw []byte, protocol string) error {
	var in struct {
		Error *struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    any    `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &in) != nil || in.Error == nil {
		return nil
	}
	code := in.Error.Type
	if in.Error.Code != nil {
		code = fmt.Sprint(in.Error.Code)
	}
	return remoteError(protocol, code, in.Error.Message)
}
