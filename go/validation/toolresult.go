package validation

import (
	"encoding/json"

	"github.com/lsm/open-agent-protocol/go/protocol"
)

func (s *state) checkResultImages(i, line int, e protocol.Envelope) {
	if s.isRequestType(e.Type) {
		return
	}
	if level, ok := s.features[protocol.FeatureContentImage]; ok && level != protocol.SupportUnavailable {
		return
	}
	var payload any
	if json.Unmarshal(e.Payload, &payload) != nil {
		return
	}
	pointer, found := "", false
	if e.Type == protocol.TypeActionCallCompleted {
		if object, ok := payload.(map[string]any); ok && carriesImage(object["result"]) {
			pointer, found = "/payload/result", true
		}
	}
	if !found {
		pointer, found = toolResultImage(payload, "/payload")
	}
	if found {
		s.addExpected(CodeUnavailableCapability, i, line, e, pointer, "a tool result carries an image part where content.image is not advertised", protocol.FeatureContentImage, "unadvertised")
	}
}

func toolResultImage(node any, pointer string) (string, bool) {
	switch value := node.(type) {
	case map[string]any:
		if value["type"] == "tool_result" && carriesImage(value["result"]) {
			return pointer + "/result", true
		}
		for key, child := range value {
			if found, ok := toolResultImage(child, pointer+"/"+key); ok {
				return found, true
			}
		}
	case []any:
		for index, child := range value {
			if found, ok := toolResultImage(child, pointer+"/"+uintString(uint64(index))); ok {
				return found, true
			}
		}
	}
	return "", false
}

func carriesImage(result any) bool {
	parts, ok := result.([]any)
	if !ok || len(parts) == 0 {
		return false
	}
	image := false
	for _, part := range parts {
		object, ok := part.(map[string]any)
		if !ok {
			return false
		}
		switch object["type"] {
		case "text":
		case "image":
			image = true
		default:
			return false
		}
	}
	return image
}
