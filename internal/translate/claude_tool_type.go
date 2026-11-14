package translate

import "strings"

// IsClaudeClientToolType reports whether a declaration type identifies an ordinary
// client tool that can be represented as a function by the cross-format adapters.
func IsClaudeClientToolType(value any) bool {
	typ, ok := value.(string)
	if !ok {
		return false
	}
	switch strings.TrimSpace(typ) {
	case "", "custom", "function":
		return true
	default:
		return false
	}
}
