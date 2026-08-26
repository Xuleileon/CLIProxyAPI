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
func PrefixCursorACPName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || strings.HasPrefix(name, CursorACPNamePrefix) {
		return name
	}
	return CursorACPNamePrefix + name
}

// cursorACPAliasInstruction is a fixed-size rule. It must not grow with the tool list.
const cursorACPAliasInstruction = `MCP tools use an "acp_" prefix (Task is acp_Task). Call acp_<original name>. Prefixed tools are available; do not treat them as missing.`

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
