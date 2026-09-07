package common

import (
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// IsGeminiThoughtPart reports whether a Gemini part contains hidden model thought.
func IsGeminiThoughtPart(part gjson.Result) bool {
	return part.Get("thought").Bool()
}

// ContentHasGeminiFunctionResponse reports whether a Gemini content turn contains any functionResponse part.
func ContentHasGeminiFunctionResponse(content []byte) bool {
	hasFR := false
	gjson.GetBytes(content, "parts").ForEach(func(_, part gjson.Result) bool {
		if part.Get("functionResponse").Exists() || part.Get("function_response").Exists() {
			hasFR = true
			return false
		}
		return true
	})
	return hasFR
}

// MergeAdjacentGeminiUserContents merges consecutive user Content turns,
// but leaves turns containing functionResponse unmerged to preserve tool-call/response boundaries.
func MergeAdjacentGeminiUserContents(contents [][]byte) [][]byte {
	if len(contents) <= 1 {
		return contents
	}
	merged := make([][]byte, 0, len(contents))
	for _, content := range contents {
		if len(content) == 0 {
			continue
		}
		role := gjson.GetBytes(content, "role").String()
		partsResult := gjson.GetBytes(content, "parts")
		if !partsResult.IsArray() || partsResult.Raw == "[]" || !partsResult.Get("0").Exists() {
			continue
		}
		if len(merged) > 0 {
			lastIndex := len(merged) - 1
			lastJSON := merged[lastIndex]
			lastRole := gjson.GetBytes(lastJSON, "role").String()
			if lastRole == "user" && role == "user" && !ContentHasGeminiFunctionResponse(lastJSON) && !ContentHasGeminiFunctionResponse(content) {
				lastParts := gjson.GetBytes(lastJSON, "parts").Array()
				currentParts := partsResult.Array()
				combinedParts := make([][]byte, 0, len(lastParts)+len(currentParts))
				for _, p := range lastParts {
					combinedParts = append(combinedParts, []byte(p.Raw))
				}
				for _, p := range currentParts {
					combinedParts = append(combinedParts, []byte(p.Raw))
				}
				updated, err := sjson.SetRawBytes(lastJSON, "parts", JoinRawArray(combinedParts))
				if err == nil {
					merged[lastIndex] = updated
					continue
				}
			}
		}
		merged = append(merged, content)
	}
	return merged
}
