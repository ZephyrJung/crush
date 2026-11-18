package proto

import (
	"github.com/charmbracelet/crush/internal/config"
)

// ConfigSetRequest represents a request to set a config field.
type ConfigSetRequest struct {
	Scope config.Scope `json:"scope"`
	Key   string       `json:"key"`
	Value any          `json:"value"`
}

// ConfigRemoveRequest represents a request to remove a config field.
type ConfigRemoveRequest struct {
	Scope config.Scope `json:"scope"`
	Key   string       `json:"key"`
}

// ConfigModelRequest represents a request to update the preferred model.
type ConfigModelRequest struct {
	Scope     config.Scope             `json:"scope"`
	ModelType config.SelectedModelType `json:"model_type"`
	Model     config.SelectedModel     `json:"model"`
}

// ConfigCompactRequest represents a request to set compact mode.
type ConfigCompactRequest struct {
	Scope   config.Scope `json:"scope"`
	Enabled bool         `json:"enabled"`
}

// ProjectNeedsInitResponse represents whether a project needs initialization.
type ProjectNeedsInitResponse struct {
	NeedsInit bool `json:"needs_init"`
}

// ProjectInitPromptResponse represents the project initialization prompt.
type ProjectInitPromptResponse struct {
	Prompt string `json:"prompt"`
}

// GitBranchResponse carries the current Git branch of the workspace's
// working directory. Branch is empty when the directory is not a Git
// repository or HEAD is detached.
type GitBranchResponse struct {
	Branch string `json:"branch"`
}

// AgentInitRequest represents a request to initialize the agent.
type AgentInitRequest struct {
	Interactive bool `json:"interactive"`
}

// AgentSetMainRequest requests switching the workspace's active agent
// (e.g. "coder" or "plan").
type AgentSetMainRequest struct {
	AgentID string `json:"agent_id"`
}

// LSPStartRequest represents a request to start an LSP for a path.
type LSPStartRequest struct {
	Path string `json:"path"`
}

// FileTrackerReadRequest represents a request to record a file read.
type FileTrackerReadRequest struct {
	SessionID string `json:"session_id"`
	Path      string `json:"path"`
}

// MCPNameRequest represents a request targeting a named MCP server.
type MCPNameRequest struct {
	Name string `json:"name"`
}

// MCPSetServerDisabledRequest toggles a repository-scoped MCP server
// override for the workspace's Toggle MCPs dialog.
type MCPSetServerDisabledRequest struct {
	Name     string `json:"name"`
	Disabled bool   `json:"disabled"`
}

// SkillSetDisabledRequest toggles a skill override: a repository-scoped
// disable, or the config's options.disabled_skills flag.
type SkillSetDisabledRequest struct {
	Name     string `json:"name"`
	Disabled bool   `json:"disabled"`
}

// MCPPendingAuthServer describes an MCP server awaiting OAuth
// authentication, returned to clients so they can prompt the user.
type MCPPendingAuthServer struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// MCPAuthResponse describes the result of an MCP authentication request.
type MCPAuthResponse struct {
	// AuthURL is the OAuth authorization URL the user must visit, when
	// the flow is still in progress.
	AuthURL string `json:"auth_url,omitempty"`
}

// MCPReadResourceRequest represents a request to read an MCP resource.
type MCPReadResourceRequest struct {
	Name string `json:"name"`
	URI  string `json:"uri"`
}

// MCPGetPromptRequest represents a request to get an MCP prompt.
type MCPGetPromptRequest struct {
	ClientID string            `json:"client_id"`
	PromptID string            `json:"prompt_id"`
	Args     map[string]string `json:"args"`
}

// MCPGetPromptResponse represents the response from getting an MCP prompt.
type MCPGetPromptResponse struct {
	Prompt string `json:"prompt"`
}
