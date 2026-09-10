// Package safeerr removes destination credentials from errors before persistence.
package safeerr

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

var urls = regexp.MustCompile(`(?i)https?://[^\s"<>]+`)
var bearer = regexp.MustCompile(`(?i)bearer\s+[^\s"<>]+`)

func Text(err error, secrets ...string) string {
	if err == nil {
		return ""
	}
	var u *url.Error
	var result string
	if errors.As(err, &u) {
		result = "HTTP request failed: " + Text(u.Err, secrets...)
	} else {
		result = err.Error()
	}
	result = urls.ReplaceAllString(result, "[destination redacted]")
	result = bearer.ReplaceAllString(result, "Bearer [redacted]")
	for _, secret := range secrets {
		if secret != "" {
			result = strings.ReplaceAll(result, secret, "[redacted]")
		}
	}
	return result
}
