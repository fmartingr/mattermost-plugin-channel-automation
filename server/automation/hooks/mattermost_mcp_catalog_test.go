package hooks

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMattermostMCPCatalog_DevToolsExcluded(t *testing.T) {
	devOnly := []string{"create_user", "create_post_as_user", "create_team", "add_user_to_team"}
	for _, name := range devOnly {
		_, ok := LookupMattermostMCPTool(name)
		assert.False(t, ok, "dev tool %q must not appear in production catalog", name)
	}
}

func TestMattermostMCPCatalog_ExpectedProductionCount(t *testing.T) {
	assert.Len(t, mattermostMCPServerTools, 20)
}

func TestMattermostMCPCatalog_DisallowedToolsPresentAndDisallowed(t *testing.T) {
	disallowed := []string{"create_post", "dm", "group_message"}
	for _, name := range disallowed {
		known, allowed := IsAllowedMattermostMCPTool(name)
		require.True(t, known, "%q must be present in the catalog", name)
		assert.False(t, allowed, "%q must be marked Allowed=false", name)
	}
}

func TestMattermostMCPCatalog_HookImplsRequireAllowed(t *testing.T) {
	for name, entry := range mattermostMCPServerTools {
		if entry.Before != nil {
			assert.True(t, entry.Allowed, "tool %q has a Before hook but is not Allowed", name)
		}
	}
}

func TestMattermostMCPCatalog_AddUserToChannel_HasBeforeHook(t *testing.T) {
	entry, ok := LookupMattermostMCPTool("add_user_to_channel")
	require.True(t, ok)
	assert.True(t, entry.Allowed)
	assert.NotNil(t, entry.Before, "add_user_to_channel must have a Before hook to enforce channel guardrails")
}

func TestMattermostMCPCatalog_CreateChannel_HasBeforeHook(t *testing.T) {
	entry, ok := LookupMattermostMCPTool("create_channel")
	require.True(t, ok)
	assert.True(t, entry.Allowed)
	assert.NotNil(t, entry.Before, "create_channel must have a Before hook to enforce team guardrails")
}

func TestIsAllowedMattermostMCPTool_UnknownTool(t *testing.T) {
	known, allowed := IsAllowedMattermostMCPTool("not_a_real_tool")
	assert.False(t, known)
	assert.False(t, allowed)
}

func TestIsCreatorOnlyMattermostMCPTool(t *testing.T) {
	creatorOnly := []string{"add_user_to_channel", "create_channel", "search_users", "list_agents", "get_user_channels"}
	for _, name := range creatorOnly {
		assert.True(t, IsCreatorOnlyMattermostMCPTool(name), "%q should be creator-only", name)
	}
	// Only the explicitly TriggererSafe channel-scoped read tools may run as
	// the triggerer. get_user_channels is excluded: it enumerates the acting
	// user's memberships and cannot be guardrailed.
	triggererSafe := []string{
		"read_post", "read_channel", "get_channel_info", "get_channel_members",
		"get_team_info", "get_team_members", "search_posts",
	}
	for _, name := range triggererSafe {
		assert.False(t, IsCreatorOnlyMattermostMCPTool(name), "%q should be triggerer-usable", name)
	}
	assert.False(t, IsCreatorOnlyMattermostMCPTool("mattermost__search_users"), "namespaced form is stripped by callers, not this helper")
	assert.False(t, IsCreatorOnlyMattermostMCPTool("not_a_real_tool"))
}

// TestCatalogFailsClosed asserts the security-critical invariant behind the
// TriggererSafe flip: every allowed tool that is not explicitly TriggererSafe
// is treated as creator-only, so a newly added tool defaults to fail-closed.
func TestCatalogFailsClosed(t *testing.T) {
	for name, entry := range mattermostMCPServerTools {
		if entry.Allowed && !entry.TriggererSafe {
			assert.True(t, IsCreatorOnlyMattermostMCPTool(name), "allowed tool %q without TriggererSafe must be creator-only", name)
		}
	}
}

func TestIsGuardrailConstrainedMattermostMCPTool(t *testing.T) {
	// Tools with a Before hook are guardrail-constrained.
	constrained := []string{"search_posts", "read_channel", "add_user_to_channel", "get_team_info", "mattermost__read_post"}
	for _, name := range constrained {
		assert.True(t, IsGuardrailConstrainedMattermostMCPTool(name), "%q should be guardrail-constrained", name)
	}
	// Embedded but unconstrained (no Before), external, and unknown are not.
	unconstrained := []string{"search_users", "list_agents", "external__search_posts", "not_a_real_tool"}
	for _, name := range unconstrained {
		assert.False(t, IsGuardrailConstrainedMattermostMCPTool(name), "%q should not be guardrail-constrained", name)
	}
}

func TestHasGuardrailConstrainedMattermostMCPTool(t *testing.T) {
	assert.True(t, HasGuardrailConstrainedMattermostMCPTool([]string{"external__search", "search_posts"}))
	assert.False(t, HasGuardrailConstrainedMattermostMCPTool([]string{"external__search", "search_users"}))
	assert.False(t, HasGuardrailConstrainedMattermostMCPTool(nil))
}
