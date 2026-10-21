// Package message defines optional origin metadata shared by senders and servers.
package message

// Context identifies where an agent notification originated. Names are display
// labels; IDs keep colors stable across renames. Never put credentials here.
type Context struct {
	Workspace   string `json:"workspace,omitempty"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	Session     string `json:"session,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
}
