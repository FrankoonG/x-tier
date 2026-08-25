package publicerr

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var codePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(?:\.[a-z][a-z0-9_]*)+$`)

var allowedNamespaces = map[string]struct{}{
	"config": {}, "control": {}, "daemon": {}, "dataplane": {}, "domain": {},
	"identity": {}, "lastgood": {}, "node": {}, "path": {}, "peer": {}, "rendradapter": {},
	"route": {}, "runtime": {}, "service": {}, "settings": {}, "topology": {}, "webbridge": {},
	"xrayconfig": {}, "xrayrt": {},
}

type coder interface {
	PublicErrorCode() string
}

type codedError struct {
	code string
	err  error
}

func (e codedError) Error() string {
	if e.err == nil {
		return e.code
	}
	return e.code + ": " + e.err.Error()
}

func (e codedError) Unwrap() error { return e.err }

func (e codedError) PublicErrorCode() string { return e.code }

// Errorf creates an error whose public code is explicit metadata. The format
// and its arguments are diagnostic detail and can never change that code.
func Errorf(code, format string, args ...any) error {
	code = NormalizeCode(code, "operation.failed")
	if format == "" {
		return codedError{code: code}
	}
	return codedError{code: code, err: fmt.Errorf(format, args...)}
}

// Wrap attaches an explicit public code while preserving errors.Is/errors.As.
func Wrap(code string, err error) error {
	if err == nil {
		return nil
	}
	return codedError{code: NormalizeCode(code, "operation.failed"), err: err}
}

// Code returns only a validated internal error code. Raw wrapped details are
// never part of the returned value.
func Code(err error, fallback string) string {
	if err == nil {
		return normalizeFallback(fallback)
	}
	var coded coder
	if errors.As(err, &coded) {
		return NormalizeCode(coded.PublicErrorCode(), fallback)
	}
	return normalizeFallback(fallback)
}

// CodeText deliberately does not infer semantics from diagnostic text.
func CodeText(_ string, fallback string) string {
	return normalizeFallback(fallback)
}

func NormalizeCode(code, fallback string) string {
	if validCode(code) {
		return code
	}
	return normalizeFallback(fallback)
}

func Message(err error, fallback string) string {
	return MessageCode(Code(err, fallback))
}

func MessageText(message, fallback string) string {
	return MessageCode(CodeText(message, fallback))
}

func MessageCode(code string) string {
	code = NormalizeCode(code, "operation.failed")
	switch code {
	case "config.credential_invalid", "config.profile_invalid", "config.credential_reuse_forbidden":
		return "profile validation failed; credential details were redacted"
	default:
		return "operation failed (" + code + ")"
	}
}

func validCode(code string) bool {
	if !codePattern.MatchString(code) {
		return false
	}
	namespace, _, found := strings.Cut(code, ".")
	if !found {
		return false
	}
	_, allowed := allowedNamespaces[namespace]
	return allowed
}

func normalizeFallback(fallback string) string {
	if validCode(fallback) {
		return fallback
	}
	return "operation.failed"
}
