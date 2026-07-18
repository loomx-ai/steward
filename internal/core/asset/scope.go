package asset

// AuthoritativeScope returns the first Region or Global scope reachable from
// scopeID. Missing and cyclic scope chains are not authoritative.
func AuthoritativeScope(scopeID ScopeID, scopes map[ScopeID]Scope) (Scope, bool) {
	currentID := scopeID
	visited := make(map[ScopeID]struct{})
	for currentID != "" {
		if _, duplicate := visited[currentID]; duplicate {
			return Scope{}, false
		}
		visited[currentID] = struct{}{}
		current, ok := scopes[currentID]
		if !ok {
			return Scope{}, false
		}
		if current.Kind == ScopeRegion || current.Kind == ScopeGlobal {
			return current, true
		}
		currentID = current.ParentID
	}
	return Scope{}, false
}
