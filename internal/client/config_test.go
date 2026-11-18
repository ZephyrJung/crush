package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/charmbracelet/crush/internal/proto"
	"github.com/stretchr/testify/require"
)

// captureClient returns a Client that talks to the given test server,
// plus a channel receiving the parsed request body for each call.
func captureClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	c, err := NewClient(t.TempDir(), "tcp", u.Host)
	require.NoError(t, err)
	return c
}

func TestListMCPPrompts(t *testing.T) {
	t.Parallel()

	want := []proto.MCPPrompt{
		{
			ID:          "server:review",
			Title:       "Review changes",
			Description: "Review the current changes.",
			PromptID:    "review",
			ClientID:    "server",
			Arguments: []proto.MCPPromptArgument{
				{ID: "focus", Title: "Focus", Description: "Area to review", Required: true},
			},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/v1/workspaces/ws1/mcp/prompts", r.URL.Path)
		require.NoError(t, json.NewEncoder(w).Encode(want))
	}))
	defer srv.Close()

	got, err := captureClient(t, srv).ListMCPPrompts(t.Context(), "ws1")
	require.NoError(t, err)
	require.Equal(t, want, got)
}

func TestListMCPPromptsNonOKStatus(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := captureClient(t, srv)
	_, err := c.ListMCPPrompts(t.Context(), "ws1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "status code 500")
}

func TestListMCPPromptsMalformedBody(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	c := captureClient(t, srv)
	_, err := c.ListMCPPrompts(t.Context(), "ws1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to decode MCP prompts")
}
