package mongo

import (
	"fmt"
	"net/url"
	"strings"
)

func isMultiHosts(uri string) (bool, error) {
	if !strings.HasPrefix(uri, "mongodb://") && !strings.HasPrefix(uri, "mongodb+srv://") {
		return false, fmt.Errorf("unsupported MongoDB URI scheme")
	}
	parsed, err := url.Parse(uri)
	if err != nil {
		return false, fmt.Errorf("parse MongoDB URI: %w", err)
	}
	if parsed.Host == "" {
		return false, fmt.Errorf("MongoDB URI host is required")
	}
	if parsed.Scheme == "mongodb+srv" {
		return true, nil
	}
	return strings.Contains(parsed.Host, ","), nil
}

func redactURI(input, replacement string) string {
	colon := strings.Index(input, ":")
	if colon == -1 || colon == len(input)-1 {
		return input
	}
	if input[colon+1] == '/' {
		for colon++; colon < len(input); colon++ {
			if input[colon] == ':' {
				break
			}
		}
		if colon == len(input) {
			return input
		}
	}

	at := strings.Index(input, "@")
	if at == -1 || at == len(input)-1 || at <= colon {
		return input
	}

	redacted := make([]byte, 0, len(input))
	for i := 0; i < len(input); i++ {
		if i <= colon || i > at {
			redacted = append(redacted, input[i])
			continue
		}
		if i == at {
			redacted = append(redacted, replacement...)
			redacted = append(redacted, input[i])
		}
	}
	return string(redacted)
}
