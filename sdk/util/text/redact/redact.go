// Package redact masks credentials in text and errors before they leave a
// connector. It depends only on the Go stdlib.
package redact

import "strings"

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
// secret replaced by Mask. The returned error unwraps to err, so errors.Is
// and errors.As keep to work. A nil err stays nil.
func Error(err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	return &redactedError{msg: Text(err.Error(), secrets...), err: err}
}

type redactedError struct {
	msg string
	err error
}

func (e *redactedError) Error() string { return e.msg }

func (e *redactedError) Unwrap() error { return e.err }
