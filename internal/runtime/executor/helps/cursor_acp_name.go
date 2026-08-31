package helps

import (
	"strings"
)

// CursorACPNamePrefix is applied to client tool names before they are sent on
// Cursor Agent Run. Cursor treats Read/Write/Bash/Agent as native IDE tools, so
// every client tool is prefixed. A short alias rule tells the model to call
// acp_<original name> when prompts mention the original name.
const CursorACPNamePrefix = "acp_"

// PrefixCursorACPName is the name Cursor sees for a client-declared tool.
// Existing prefixes receive another layer so removing one layer restores the
// exact name declared by the client.
func PrefixCursorACPName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return name
	}
	return CursorACPNamePrefix + name
}

// cursorACPAliasInstruction is a fixed-size rule. It must not grow with the tool list.
const cursorACPAliasInstruction = `MCP tools use an "acp_" prefix (Grep is acp_Grep, Read is acp_Read, Bash is acp_Bash, Task is acp_Task). Call acp_<original name>. Prefixed tools are available.`

// AnnotateCursorACPDescription prefixes a short original-name tag so subagent
// prompts that say "use Grep" can bind to the acp_Grep MCP schema.
func AnnotateCursorACPDescription(originalName, description string) string {
	originalName = strings.TrimSpace(originalName)
	description = strings.TrimSpace(description)
	if originalName == "" {
		return description
	}
	tag := "[" + originalName + "]"
	if description == "" {
		return tag
	}
	if strings.HasPrefix(description, tag) {
		return description
	}
	return tag + " " + description
}

// CursorACPAliasInstruction returns a constant alias rule when any client tools are present.
func CursorACPAliasInstruction(originalNames []string) string {
	for _, name := range originalNames {
		if strings.TrimSpace(name) != "" {
			return cursorACPAliasInstruction
		}
	}
	return ""
}

// UnprefixCursorACPName restores the name the downstream client declared.
func UnprefixCursorACPName(name string) string {
	return strings.TrimPrefix(strings.TrimSpace(name), CursorACPNamePrefix)
}

// CursorACPNameEquals reports whether name is alias, with or without the ACP prefix.
func CursorACPNameEquals(name, alias string) bool {
	return strings.EqualFold(UnprefixCursorACPName(name), strings.TrimSpace(alias))
}
