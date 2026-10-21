package api

import (
	"meow.blue/meow/message"
	"net/http"
	"strings"
	"unicode"
)

func checkContext(w http.ResponseWriter, c *message.Context) bool {
	if c == nil {
		return true
	}
	for _, field := range []struct {
		name  string
		value *string
		max   int
	}{
		{"context.workspace", &c.Workspace, 200}, {"context.workspace_id", &c.WorkspaceID, 128},
		{"context.session", &c.Session, 200}, {"context.session_id", &c.SessionID, 128},
	} {
		*field.value = strings.TrimSpace(*field.value)
		if !checkText(w, field.name, *field.value, false, field.max) {
			return false
		}
		if strings.ContainsFunc(*field.value, unicode.IsControl) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_request", field.name+" must be a single line without control characters")
			return false
		}
	}
	return true
}
