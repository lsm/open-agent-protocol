package validation

import (
	"encoding/json"
	"maps"
	"slices"

	"github.com/lsm/open-agent-protocol/go/protocol"
)

func (s *state) checkResultImages(i, line int, e protocol.Envelope) {
	if s.isRequestType(e.Type) {
		return
	}
	if s.advertised(protocol.FeatureContentImage) {
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
		for _, key := range slices.Sorted(maps.Keys(value)) {
			if found, ok := toolResultImage(value[key], pointer+"/"+key); ok {
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
		if !ok || len(object) != 2 {
			return false
		}
		switch object["type"] {
		case "text":
			if _, ok := object["text"].(string); !ok {
				return false
			}
		case "image":
			if !imageSource(object["image"]) {
				return false
			}
			image = true
		default:
			return false
		}
	}
	return image
}

func imageSource(value any) bool {
	source, ok := value.(map[string]any)
	if !ok {
		return false
	}
	filled := func(key string) bool {
		text, ok := source[key].(string)
		return ok && text != ""
	}
	switch len(source) {
	case 1:
		return filled("url")
	case 2:
		return filled("data") && filled("media_type")
	}
	return false
}
