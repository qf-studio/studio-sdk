// Package redact masks credentials in text and errors before they leave a
// connector. It depends only on the Go stdlib.
package redact

import (
	"errors"
	"fmt"
	"net/url"
	"reflect"
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
// hold the secret, so a host that walks the chain would get it. errors.Is on
// the returned error answers as it would on err. errors.As finds the same
// match as it would on err, but never hands out a value whose text, or the
// text of an error in its Unwrap chain, holds a secret:
//   - A *url.Error match, for a *url.Error target or an interface target such
//     as net.Error, is handed out as a copy whose URL is masked and whose Err
//     is masked with Error; err itself is not changed.
//   - Any other match whose chain holds a secret makes errors.As answer false
//     and leaves the target zero. This includes an fmt wrapper matched by an
//     interface target such as interface{ Unwrap() error }.
//   - A match with no secret in its chain is handed out as it is.
//
// Printing the returned error with any fmt verb, %+v and %#v included, gives
// only the masked text.
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

// Format prints only the masked text, so no verb (%+v, %#v, ...) prints the
// cause or the secrets.
func (e *redactedError) Format(f fmt.State, verb rune) {
	fmt.Fprintf(f, fmt.FormatString(f, verb), e.msg)
}

// Is reports whether the cause matches target.
func (e *redactedError) Is(target error) bool { return errors.Is(e.err, target) }

// As finds the first error in the cause chain that matches target. A
// *url.Error is handed out as a masked copy, whether target points to a
// *url.Error or to an interface type it satisfies (such as net.Error). Any
// match whose chain still holds a secret is not handed out: target is reset
// to its zero value and As answers false.
func (e *redactedError) As(target any) bool {
	if !errors.As(e.err, target) {
		return false
	}
	// errors.As has checked that target is a non-nil pointer.
	v := reflect.ValueOf(target).Elem()
	if u, ok := v.Interface().(*url.Error); ok && u != nil {
		masked := *u
		masked.URL = Text(masked.URL, e.secrets...)
		masked.Err = Error(masked.Err, e.secrets...)
		v.Set(reflect.ValueOf(&masked))
	}
	if match, ok := v.Interface().(error); ok && e.leaks(match) {
		v.Set(reflect.Zero(v.Type()))
		return false
	}
	return true
}

// leaks reports whether the text of err, or of an error in its Unwrap chain,
// holds one of the secrets.
func (e *redactedError) leaks(err error) bool {
	for err != nil {
		if e.holdsSecret(err.Error()) {
			return true
		}
		switch x := err.(type) {
		case interface{ Unwrap() []error }:
			for _, branch := range x.Unwrap() {
				if e.leaks(branch) {
					return true
				}
			}
			return false
		case interface{ Unwrap() error }:
			err = x.Unwrap()
		default:
			return false
		}
	}
	return false
}

func (e *redactedError) holdsSecret(s string) bool {
	for _, secret := range e.secrets {
		if secret != "" && strings.Contains(s, secret) {
			return true
		}
	}
	return false
}
