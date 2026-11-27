// Package redact masks credentials in text and errors before they leave a
// connector. It depends only on the Go stdlib.
package redact

import (
	"errors"
	"net/url"
	"strings"
)

// Mask replaces each secret value in the masked text.
const Mask = "[REDACTED]"

// Text returns s with each non-empty secret replaced by Mask, wherever it
// stands in s.
func Text(s string, secrets ...string) string {
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		s = strings.ReplaceAll(s, secret, Mask)
	}
	return s
}

// Error returns an error whose text is the text of err with each non-empty
// secret replaced by Mask. A nil err stays nil.
//
// The returned error does not unwrap to err: the text of each cause can still
// hold the secret, so a host that walks the chain would get it. errors.Is and
// errors.As on the returned error answer as they would on err. A *url.Error
// that errors.As hands out is a copy whose URL is masked and whose Err is
// masked with Error; err itself is not changed.
func Error(err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	return &redactedError{msg: Text(err.Error(), secrets...), err: err, secrets: secrets}
}

type redactedError struct {
	msg     string
	err     error
	secrets []string
}

func (e *redactedError) Error() string { return e.msg }

// Is reports whether the cause matches target.
func (e *redactedError) Is(target error) bool { return errors.Is(e.err, target) }

// As finds the first error in the cause chain that matches target. A
// *url.Error is handed out as a masked copy.
func (e *redactedError) As(target any) bool {
	if !errors.As(e.err, target) {
		return false
	}
	if p, ok := target.(**url.Error); ok && *p != nil {
		masked := **p
		masked.URL = Text(masked.URL, e.secrets...)
		masked.Err = Error(masked.Err, e.secrets...)
		*p = &masked
	}
	return true
}
