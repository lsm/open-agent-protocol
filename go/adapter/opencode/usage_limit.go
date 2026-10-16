package opencode

import (
	"encoding/json"
	"strings"

	"github.com/lsm/open-agent-protocol/go/adapter/opencode/internal/native"
)

func failureText(failure native.SessionError) string {
	if failure.Response == nil {
		return failure.Message
	}
	var body struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
		Metadata struct {
			LimitName string `json:"limitName"`
		} `json:"metadata"`
	}
	if json.Unmarshal([]byte(failure.Response.Body), &body) != nil || !strings.HasSuffix(body.Error.Type, "UsageLimitError") {
		return failure.Message
	}
	var details []string
	if body.Metadata.LimitName != "" {
		details = append(details, body.Metadata.LimitName+" limit")
	}
	if reset := resetIn(body.Error.Message); reset != "" {
		details = append(details, "resets in "+reset)
	}
	if len(details) == 0 {
		return failure.Message
	}
	return failure.Message + "; " + strings.Join(details, ", ")
}

func resetIn(message string) string {
	lowered := strings.ToLower(message)
	for _, lead := range []string{"resets in ", "retry in "} {
		at := strings.Index(lowered, lead)
		if at < 0 {
			continue
		}
		rest := message[at+len(lead):]
		if end := strings.Index(rest, "."); end >= 0 {
			rest = rest[:end]
		}
		return spelledDuration(rest)
	}
	return ""
}

func spelledDuration(text string) string {
	fields := strings.Fields(text)
	for i, field := range fields {
		switch {
		case strings.HasSuffix(field, "hr"):
			fields[i] = unitCount(strings.TrimSuffix(field, "hr"), "hour")
		case strings.HasSuffix(field, "min"):
			fields[i] = unitCount(strings.TrimSuffix(field, "min"), "minute")
		}
	}
	return strings.Join(fields, " ")
}

func unitCount(count, unit string) string {
	if count == "1" {
		return "1 " + unit
	}
	return count + " " + unit + "s"
}
