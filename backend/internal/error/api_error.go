package apierror

import (
	"errors"
	"net/http"
)

// Code 表示 HTTP 错误响应的公共类别，决定状态码与默认文案
type Code string

const (
	CodeInvalid      Code = "invalid"
	CodeUnauthorized Code = "unauthorized"
	CodeForbidden    Code = "forbidden"
	CodeNotFound     Code = "not_found"
	CodeConflict     Code = "conflict"
	CodeTooLarge     Code = "too_large"
	CodeRateLimited  Code = "rate_limited"
	CodeUnavailable  Code = "unavailable"
	CodeInternal     Code = "internal"
)

// Descriptor 描述错误响应的公共类别、对外文案与底层原因
// Cause 只用于日志、追踪和 errors.Is，不会写入响应
type Descriptor struct {
	Code          Code
	PublicMessage string
	Cause         error
}

// Rule 把模块领域错误映射到公共类别，按传入顺序从最具体到最通用匹配
// UseErrorText 为真时对外使用错误自身文案，仅限已确认可安全暴露的领域错误
type Rule struct {
	Match         func(error) bool
	Code          Code
	PublicMessage string
	UseErrorText  bool
}

// Is 返回按 errors.Is 匹配任一目标错误的规则匹配函数
func Is(targets ...error) func(error) bool {
	return func(err error) bool {
		for _, target := range targets {
			if target != nil && errors.Is(err, target) {
				return true
			}
		}
		return false
	}
}

// Resolve 按规则顺序解析错误，未匹配时降级为 500 内部错误
// fallback 是未知错误的模块级安全文案，不会回显底层错误细节
func Resolve(err error, fallback string, rules ...Rule) Descriptor {
	if err == nil {
		// 成功响应由具体 handler 显式写入，这里只做防御性降级，避免生成 200
		return Descriptor{Code: CodeInternal, PublicMessage: fallbackMessage(fallback)}
	}
	for _, rule := range rules {
		if rule.Match == nil || !rule.Match(err) {
			continue
		}
		return Descriptor{Code: rule.Code, PublicMessage: ruleMessage(rule, err), Cause: err}
	}
	return Descriptor{Code: CodeInternal, PublicMessage: fallbackMessage(fallback), Cause: err}
}

// HTTPStatus 是公共错误类别到 HTTP 状态码的唯一映射点
func HTTPStatus(code Code) int {
	switch code {
	case CodeInvalid:
		return http.StatusBadRequest
	case CodeUnauthorized:
		return http.StatusUnauthorized
	case CodeForbidden:
		return http.StatusForbidden
	case CodeNotFound:
		return http.StatusNotFound
	case CodeConflict:
		return http.StatusConflict
	case CodeTooLarge:
		return http.StatusRequestEntityTooLarge
	case CodeRateLimited:
		return http.StatusTooManyRequests
	case CodeUnavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// ruleMessage 决定单条规则对外的文案：显式文案优先，其次错误自身文案，最后按类别兜底
func ruleMessage(rule Rule, err error) string {
	if rule.PublicMessage != "" {
		return rule.PublicMessage
	}
	if rule.UseErrorText {
		return err.Error()
	}
	return defaultMessage(rule.Code)
}

// fallbackMessage 保证未知错误始终有可对外的模块级文案
func fallbackMessage(fallback string) string {
	if fallback == "" {
		return defaultMessage(CodeInternal)
	}
	return fallback
}

// defaultMessage 给出各公共类别的安全默认文案，避免把内部错误文本直接对外
func defaultMessage(code Code) string {
	switch code {
	case CodeInvalid:
		return "invalid request"
	case CodeUnauthorized:
		return "unauthorized"
	case CodeForbidden:
		return "forbidden"
	case CodeNotFound:
		return "resource not found"
	case CodeConflict:
		return "resource conflict"
	case CodeTooLarge:
		return "request entity too large"
	case CodeRateLimited:
		return "rate limit exceeded"
	case CodeUnavailable:
		return "service temporarily unavailable"
	default:
		return "internal server error"
	}
}
