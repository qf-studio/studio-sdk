package redact

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"strings"
	"testing"
)

func TestText(t *testing.T) {
	tests := []struct {
		name    string
		s       string
		secrets []string
		want    string
	}{
		{name: "no secrets", s: "abc", want: "abc"},
		{name: "single", s: "url /botTOKEN/getUpdates", secrets: []string{"TOKEN"}, want: "url /bot" + Mask + "/getUpdates"},
		{name: "each occurrence", s: "TOKEN and TOKEN", secrets: []string{"TOKEN"}, want: Mask + " and " + Mask},
		{name: "many secrets", s: "a=one b=two", secrets: []string{"one", "two"}, want: "a=" + Mask + " b=" + Mask},
		{name: "empty secret ignored", s: "abc", secrets: []string{""}, want: "abc"},
		{name: "secret absent", s: "abc", secrets: []string{"xyz"}, want: "abc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Text(tt.s, tt.secrets...); got != tt.want {
				t.Errorf("Text() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestError_Nil(t *testing.T) {
	if err := Error(nil, "secret"); err != nil {
		t.Errorf("Error(nil) = %v, want nil", err)
	}
}

func TestError_MasksText(t *testing.T) {
	cause := fmt.Errorf("get https://host/botSECRET/x: %w", errors.New("dial failed"))
	err := Error(cause, "SECRET")
	if strings.Contains(err.Error(), "SECRET") {
		t.Errorf("Error() = %q, still holds the secret", err.Error())
	}
	want := "get https://host/bot" + Mask + "/x: dial failed"
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}

func TestError_KeepsIsAndAs(t *testing.T) {
	sentinel := errors.New("sentinel")
	pathErr := &fs.PathError{Op: "open", Path: "/data/file", Err: sentinel}
	err := Error(fmt.Errorf("wrap SECRET: %w", pathErr), "SECRET")

	if !errors.Is(err, sentinel) {
		t.Error("errors.Is(err, sentinel) = false, want true")
	}
	var target *fs.PathError
	if !errors.As(err, &target) {
		t.Fatal("errors.As(err, *fs.PathError) = false, want true")
	}
	if target != pathErr {
		t.Error("errors.As returned a different *fs.PathError")
	}
}

func TestError_AsRefusesSecretInPathError(t *testing.T) {
	sentinel := errors.New("sentinel")
	pathErr := &fs.PathError{Op: "open", Path: "/SECRET", Err: sentinel}
	err := Error(fmt.Errorf("wrap: %w", pathErr), "SECRET")

	if !errors.Is(err, sentinel) {
		t.Error("errors.Is(err, sentinel) = false, want true")
	}
	var target *fs.PathError
	if errors.As(err, &target) {
		t.Fatalf("errors.As(err, *fs.PathError) = true with %q, want false", target.Error())
	}
	if target != nil {
		t.Errorf("target = %v, want nil", target)
	}
}

// customErr is a custom error type for the errors.As test; its text holds the
// secret.
type customErr struct{ code int }

func (e *customErr) Error() string { return fmt.Sprintf("custom %d SECRET", e.code) }

// apiErr is a custom error type whose text is its Body field.
type apiErr struct{ Body string }

func (e *apiErr) Error() string { return "api: " + e.Body }

func TestError_IsSentinelInCause(t *testing.T) {
	sentinel := errors.New("sentinel")
	err := Error(fmt.Errorf("wrap SECRET: %w", sentinel), "SECRET")
	if !errors.Is(err, sentinel) {
		t.Error("errors.Is(err, sentinel) = false, want true")
	}
	if errors.Is(err, errors.New("other")) {
		t.Error("errors.Is(err, other) = true, want false")
	}
}

func TestError_AsCustomTypeWithSecret(t *testing.T) {
	err := Error(fmt.Errorf("wrap: %w", &customErr{code: 7}), "SECRET")
	var target *customErr
	if errors.As(err, &target) {
		t.Fatalf("errors.As(err, *customErr) = true with %q, want false", target.Error())
	}
	if target != nil {
		t.Errorf("target = %v, want nil", target)
	}
}

func TestError_AsCustomTypeWithSecretInField(t *testing.T) {
	err := Error(fmt.Errorf("wrap: %w", &apiErr{Body: "echo SECRET"}), "SECRET")
	var target *apiErr
	if errors.As(err, &target) {
		t.Fatalf("errors.As(err, *apiErr) = true with Body %q, want false", target.Body)
	}
	if target != nil {
		t.Errorf("target = %v, want nil", target)
	}
}

func TestError_AsCustomTypeWithoutSecret(t *testing.T) {
	cause := &apiErr{Body: "not found"}
	err := Error(fmt.Errorf("wrap SECRET: %w", cause), "SECRET")
	var target *apiErr
	if !errors.As(err, &target) {
		t.Fatal("errors.As(err, *apiErr) = false, want true")
	}
	if target != cause {
		t.Errorf("errors.As gave %v, want %v", target, cause)
	}
}

// TestError_AsUnwrapInterface uses the chain the Telegram client builds:
// fmt.Errorf("failed to get updates: %w", *url.Error). The fmt wrapper is the
// first match for an Unwrap interface target, and its text holds the secret.
func TestError_AsUnwrapInterface(t *testing.T) {
	inner := fmt.Errorf("dial https://host/botSECRET/x: %w", errors.New("refused"))
	orig := &url.Error{Op: "Get", URL: "https://host/botSECRET/getUpdates", Err: inner}
	err := Error(fmt.Errorf("failed to get updates: %w", orig), "SECRET")

	var u interface{ Unwrap() error }
	if !errors.As(err, &u) {
		return
	}
	// As answered true: no error in the chain of the value may hold the secret.
	for e := error(u.(error)); e != nil; e = errors.Unwrap(e) {
		if strings.Contains(e.Error(), "SECRET") {
			t.Fatalf("errors.As(err, Unwrap iface) gave %T with text %q", e, e.Error())
		}
	}
}

func TestError_FormatHidesSecrets(t *testing.T) {
	err := Error(fmt.Errorf("get https://host/botSECRET/x: %w", errors.New("dial")), "SECRET")
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%d", "%x"} {
		got := fmt.Sprintf(verb, err)
		if strings.Contains(got, "SECRET") {
			t.Errorf("Sprintf(%s) = %q, holds the secret", verb, got)
		}
		if verb == "%v" || verb == "%+v" || verb == "%s" {
			if got != err.Error() {
				t.Errorf("Sprintf(%s) = %q, want %q", verb, got, err.Error())
			}
		}
	}
}

func TestError_AsURLErrorIsMasked(t *testing.T) {
	inner := fmt.Errorf("dial https://host/botSECRET/x: %w", errors.New("refused"))
	orig := &url.Error{Op: "Get", URL: "https://host/botSECRET/getUpdates", Err: inner}
	err := Error(fmt.Errorf("failed to get updates: %w", orig), "SECRET")

	var target *url.Error
	if !errors.As(err, &target) {
		t.Fatal("errors.As(err, *url.Error) = false, want true")
	}
	if target == orig {
		t.Fatal("errors.As gave the original *url.Error, want a masked copy")
	}
	if target.Op != "Get" {
		t.Errorf("Op = %q, want %q", target.Op, "Get")
	}
	if want := "https://host/bot" + Mask + "/getUpdates"; target.URL != want {
		t.Errorf("URL = %q, want %q", target.URL, want)
	}
	if strings.Contains(target.Error(), "SECRET") {
		t.Errorf("url.Error text %q holds the secret", target.Error())
	}
	if target.Err == nil || strings.Contains(target.Err.Error(), "SECRET") {
		t.Errorf("url.Error Err %v holds the secret", target.Err)
	}
	if errors.Unwrap(target.Err) != nil {
		t.Error("url.Error Err unwraps to its unmasked cause")
	}

	// The original error is not changed.
	if orig.URL != "https://host/botSECRET/getUpdates" || orig.Err != inner {
		t.Errorf("original *url.Error changed: %+v", orig)
	}
}

func TestError_AsInterfaceGetsMaskedURLError(t *testing.T) {
	orig := &url.Error{Op: "Get", URL: "https://host/botSECRET/getUpdates", Err: errors.New("dial SECRET")}
	err := Error(fmt.Errorf("failed to get updates: %w", orig), "SECRET")

	var ne net.Error
	if !errors.As(err, &ne) {
		t.Fatal("errors.As(err, net.Error) = false, want true")
	}
	var timeouter interface{ Timeout() bool }
	if !errors.As(err, &timeouter) {
		t.Fatal("errors.As(err, interface{ Timeout() bool }) = false, want true")
	}
	for name, got := range map[string]any{"net.Error": ne, "Timeout": timeouter} {
		u, ok := got.(*url.Error)
		if !ok {
			t.Fatalf("%s: got %T, want *url.Error", name, got)
		}
		if u == orig {
			t.Errorf("%s: errors.As gave the original *url.Error, want a masked copy", name)
		}
		if strings.Contains(u.Error(), "SECRET") {
			t.Errorf("%s: text %q holds the secret", name, u.Error())
		}
	}
	if orig.URL != "https://host/botSECRET/getUpdates" {
		t.Errorf("original *url.Error changed: %+v", orig)
	}
}

func TestError_DoesNotUnwrap(t *testing.T) {
	cause := fmt.Errorf("wrap SECRET: %w", errors.New("inner"))
	err := Error(cause, "SECRET")
	if got := errors.Unwrap(err); got != nil {
		t.Errorf("errors.Unwrap(err) = %v, want nil", got)
	}
	if cause.Error() != "wrap SECRET: inner" {
		t.Errorf("cause changed: %q", cause.Error())
	}
}
