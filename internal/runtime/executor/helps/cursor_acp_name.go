package helps

import "strings"

// CursorACPNamePrefix is applied to client tool names before they are sent on
// Cursor Agent Run. Cursor treats Read/Write/Bash/Agent as native IDE tools.
const CursorACPNamePrefix = "acp_"

// PrefixCursorACPName is the name Cursor sees for a client-declared tool.
func PrefixCursorACPName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || strings.HasPrefix(name, CursorACPNamePrefix) {
		return name
	}
	return CursorACPNamePrefix + name
}

// UnprefixCursorACPName restores the name the downstream client declared.
func UnprefixCursorACPName(name string) string {
	return strings.TrimPrefix(strings.TrimSpace(name), CursorACPNamePrefix)
}

// CursorACPNameEquals reports whether name is alias, with or without the ACP prefix.
func CursorACPNameEquals(name, alias string) bool {
	return strings.EqualFold(UnprefixCursorACPName(name), strings.TrimSpace(alias))
}
