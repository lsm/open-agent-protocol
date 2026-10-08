package sdk

import (
	"net/url"
	"strings"
)

const (
	maxModelRefLength   = 4096
	maxIdentifierLength = 256
	maxModelFieldLength = 512
	maxOpaqueRefLength  = maxModelFieldLength
)

func validateExecutionRequest(modelRef string, messages []Message) error {
	if modelRef == "" {
		return &ProtocolError{Code: CodeInvalidRequest, Message: "request requires an opaque model_ref"}
	}
	if len(modelRef) > maxModelRefLength {
		return &ProtocolError{
			Code:    CodeInvalidRequest,
			Message: "model_ref exceeds the maximum length of 4096 characters",
		}
	}
	if messages == nil {
		return &ProtocolError{Code: CodeInvalidRequest, Message: "request requires messages"}
	}
	parsed, ok := parseModelRef(modelRef)
	if !ok {
		parsed, ok = splitModelRefLoosely(modelRef)
	}
	if !ok {
		if len(modelRef) > maxOpaqueRefLength {
			return &ProtocolError{
				Code:    CodeInvalidRequest,
				Message: "model_ref exceeds the maximum length of 512 characters for opaque refs",
			}
		}
		return nil
	}
	switch {
	case len(parsed.providerID) > maxIdentifierLength:
		return &ProtocolError{Code: CodeInvalidRequest, Message: "model_ref provider segment exceeds the maximum length of 256 characters"}
	case len(parsed.api) > maxIdentifierLength:
		return &ProtocolError{Code: CodeInvalidRequest, Message: "model_ref api segment exceeds the maximum length of 256 characters"}
	case len(parsed.modelID) > maxModelFieldLength:
		return &ProtocolError{Code: CodeInvalidRequest, Message: "model_ref model_id segment exceeds the maximum length of 512 characters"}
	}
	return nil
}

type parsedModelRef struct {
	providerID string
	api        string
	modelID    string
}

func parseModelRef(modelRef string) (parsedModelRef, bool) {
	slash := strings.Index(modelRef, "/")
	if slash <= 0 {
		return parsedModelRef{}, false
	}
	if at := strings.Index(modelRef, "@"); at != -1 && at < slash {
		return parsedModelRef{}, false
	}
	rest := modelRef[slash+1:]
	atOffset := strings.Index(rest, "@")
	if atOffset == -1 {
		return parsedModelRef{}, false
	}
	api := rest[:atOffset]
	encodedID := rest[atOffset+1:]
	providerID := modelRef[:slash]

	if api == "" || encodedID == "" {
		return parsedModelRef{}, false
	}
	if strings.ContainsAny(api, "/@%") || strings.ContainsAny(providerID, "/@%") {
		return parsedModelRef{}, false
	}
	if !isCanonicallyEncoded(encodedID) {
		return parsedModelRef{}, false
	}
	modelID, err := url.PathUnescape(encodedID)
	if err != nil {
		return parsedModelRef{}, false
	}
	return parsedModelRef{providerID: providerID, api: api, modelID: modelID}, true
}

func isCanonicallyEncoded(segment string) bool {
	for i := 0; i < len(segment); i++ {
		char := segment[i]
		if char == '%' {
			if i+2 >= len(segment) || !isHexDigit(segment[i+1]) || !isHexDigit(segment[i+2]) {
				return false
			}
			i += 2
			continue
		}
		if !isUnreserved(char) {
			return false
		}
	}
	return true
}

func isHexDigit(char byte) bool {
	return (char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')
}

func isUnreserved(char byte) bool {
	switch {
	case char >= 'A' && char <= 'Z', char >= 'a' && char <= 'z', char >= '0' && char <= '9':
		return true
	case char == '-' || char == '.' || char == '_' || char == '~':
		return true
	default:
		return false
	}
}

func splitModelRefLoosely(modelRef string) (parsedModelRef, bool) {
	slash := strings.Index(modelRef, "/")
	at := strings.Index(modelRef, "@")
	if slash == -1 || at == -1 || slash >= at {
		return parsedModelRef{}, false
	}
	providerID := modelRef[:slash]
	api := modelRef[slash+1 : at]
	if providerID == "" || api == "" {
		return parsedModelRef{}, false
	}
	return parsedModelRef{providerID: providerID, api: api, modelID: modelRef[at+1:]}, true
}

func providerIDFromRef(modelRef string) string {
	if parsed, ok := parseModelRef(modelRef); ok {
		return parsed.providerID
	}
	if parsed, ok := splitModelRefLoosely(modelRef); ok {
		return parsed.providerID
	}
	return ""
}
