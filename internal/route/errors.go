package route

import "fmt"

type CompileError struct {
	Code    string
	Path    string
	Details string
}

func (e *CompileError) Error() string {
	if e.Path == "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Details)
	}
	return fmt.Sprintf("%s: %s: %s", e.Code, e.Path, e.Details)
}

func (e *CompileError) PublicErrorCode() string { return e.Code }

func errf(code, path, format string, args ...any) error {
	return &CompileError{Code: code, Path: path, Details: fmt.Sprintf(format, args...)}
}
