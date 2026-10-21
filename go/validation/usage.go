package validation

import "encoding/json"

func noncanonicalUsage(value any) string {
	envelope, _ := value.(map[string]any)
	switch envelope["type"] {
	case "content.completed", "run.completed", "run.failed", "run.cancelled", "inference.completed", "inference.failed":
	default:
		return ""
	}
	payload, _ := envelope["payload"].(map[string]any)
	usage, _ := payload["usage"].(map[string]any)
	for _, field := range []string{"input_tokens", "output_tokens", "total_tokens", "cache_read_input_tokens", "cache_write_input_tokens", "reasoning_tokens"} {
		number, ok := usage[field].(json.Number)
		if !ok {
			continue
		}
		token := number.String()
		if token == "0" {
			continue
		}
		if len(token) == 0 || token[0] < '1' || token[0] > '9' {
			return field
		}
		for _, digit := range token {
			if digit < '0' || digit > '9' {
				return field
			}
		}
	}
	return ""
}
