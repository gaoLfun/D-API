package safeerr

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func TestTextRedactsWrappedDestinationsAndCredentials(t *testing.T) {
	for _, err := range []error{
		&url.Error{Op: "Post", URL: "https://hooks.example/path-secret?key=query-secret", Err: errors.New("connection refused")},
		fmt.Errorf("send: %w", &url.Error{Op: "Post", URL: "https://user:password@hooks.example/path-secret", Err: errors.New("failed")}),
		errors.New("request https://hooks.example/path-secret?key=query-secret failed Bearer header-secret bare-secret"),
	} {
		got := Text(err, "bare-secret")
		for _, secret := range []string{"path-secret", "query-secret", "password", "header-secret", "bare-secret"} {
			if strings.Contains(got, secret) {
				t.Errorf("leaked %s", secret)
			}
		}
		if got == "" {
			t.Fatal("lost diagnostic")
		}
	}
}
