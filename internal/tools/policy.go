package tools

// Policy defines allow/deny rules for tool execution.
type Policy struct {
	Allow []string // tool names to allow (empty = allow all)
	Deny  []string // tool names to deny (checked after allow)
}

// IsAllowed checks whether a tool name is permitted by this policy.
// Logic: if Allow is non-empty, the tool must be in Allow.
// If the tool is in Deny, it is blocked regardless.
func (p Policy) IsAllowed(toolName string) bool {
	// Check deny list first
	for _, d := range p.Deny {
		if d == toolName || d == "*" {
			return false
		}
	}

	// If allow list is non-empty, tool must be in it
	if len(p.Allow) > 0 {
		for _, a := range p.Allow {
			if a == toolName || a == "*" {
				return true
			}
		}
		return false
	}

	return true
}
