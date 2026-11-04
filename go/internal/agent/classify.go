package agent

import (
	"regexp"
	"strconv"
	"strings"
)

type errorClass int

const (
	errorFatal errorClass = iota
	errorTransient
	errorThrottled
	errorContextOverflow
)

var httpStatus = regexp.MustCompile(`HTTP (\d+)`)

var statusWithoutBody = regexp.MustCompile(`(400|413).*\(no body\)`)

var overflowTexts = []string{
	"prompt is too long",
	"input is too long for requested model",
	"exceeds the context window",
	"maximum prompt length is",
	"reduce the length of the messages",
	"maximum context length is",
	"exceeds the limit of",
	"exceeds the available context size",
	"greater than the context length",
	"context window exceeds limit",
	"exceeded model token limit",
	"context_length_exceeded",
	"context length exceeded",
	"too many tokens",
	"token limit exceeded",
}

var throttledTexts = []string{"rate limit", "ratelimit", "too many requests", "resource exhausted"}

var transientTexts = []string{"overloaded", "service unavailable", "temporarily unavailable", "try again", "retry later", "other side closed", "read error", "read failed", "receive failed", "timed out", "connection reset", "unexpected eof"}

func classify(reason string) errorClass {
	text := strings.ToLower(reason)
	if containsAny(text, overflowTexts) || statusWithoutBody.MatchString(text) || strings.Contains(text, "input token count") && strings.Contains(text, "exceeds the maximum") {
		return errorContextOverflow
	}
	if match := httpStatus.FindStringSubmatch(reason); match != nil {
		status, _ := strconv.Atoi(match[1])
		switch status {
		case 429:
			return errorThrottled
		case 500, 502, 503, 504:
			return errorTransient
		}
		return errorFatal
	}
	if containsAny(text, throttledTexts) {
		return errorThrottled
	}
	if containsAny(text, transientTexts) {
		return errorTransient
	}
	return errorFatal
}

func retryable(reason string) bool {
	class := classify(reason)
	return class == errorTransient || class == errorThrottled
}

func containsAny(text string, needles []string) bool {
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}
