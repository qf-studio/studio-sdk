package redact

import (
	"errors"
	"fmt"
	"io/fs"
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
	pathErr := &fs.PathError{Op: "open", Path: "/SECRET", Err: sentinel}
	err := Error(fmt.Errorf("wrap: %w", pathErr), "SECRET")

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

// customErr is a custom error type for the errors.As test.
type customErr struct{ code int }

func (e *customErr) Error() string { return fmt.Sprintf("custom %d SECRET", e.code) }

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

func TestError_AsCustomType(t *testing.T) {
	cause := &customErr{code: 7}
	err := Error(fmt.Errorf("wrap: %w", cause), "SECRET")
	var target *customErr
	if !errors.As(err, &target) {
		t.Fatal("errors.As(err, *customErr) = false, want true")
	}
	if target != cause || target.code != 7 {
		t.Errorf("errors.As gave %v, want %v", target, cause)
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
