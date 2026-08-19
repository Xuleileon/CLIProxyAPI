package helps

import (
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
)

func hasGeminiFamilyUsageTokenFields(node gjson.Result) bool {
	return node.Get("promptTokenCount").Exists() ||
		node.Get("candidatesTokenCount").Exists() ||
		node.Get("thoughtsTokenCount").Exists() ||
		node.Get("totalTokenCount").Exists() ||
		node.Get("cachedContentTokenCount").Exists()
}

func ParseGeminiCLIUsage(data []byte) usage.Detail {
	root := gjson.ParseBytes(data)
	node := firstExistingUsageNode(root, "response.usageMetadata", "response.usage_metadata", "usageMetadata", "usage_metadata")
	if !node.Exists() {
		return usage.Detail{}
	}
	return parseGeminiFamilyUsageDetail(node)
}

func ParseGeminiCLIStreamUsage(line []byte) (usage.Detail, bool) {
	payload := jsonPayload(line)
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return usage.Detail{}, false
	}
	node := firstExistingUsageNode(gjson.ParseBytes(payload), "response.usageMetadata", "response.usage_metadata", "usageMetadata", "usage_metadata")
	if !node.Exists() || !hasGeminiFamilyUsageTokenFields(node) {
		return usage.Detail{}, false
	}
	return parseGeminiFamilyUsageDetail(node), true
}
