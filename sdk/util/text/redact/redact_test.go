package redact

import (
	"errors"
	"fmt"
	"io/fs"
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

func TestError_Unwraps(t *testing.T) {
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
