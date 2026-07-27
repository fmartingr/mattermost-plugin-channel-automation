package hooks

import (
	"errors"
	"fmt"
	"testing"

	"github.com/mattermost/mattermost-plugin-agents/public/bridgeclient"
	mmmodel "github.com/mattermost/mattermost/server/public/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mattermost/mattermost-plugin-channel-automation/server/model"
)

type stubLister struct {
	tools []bridgeclient.BridgeToolInfo
	err   error

	// calls records every (agentID, userID) pair the validator passed to
	// GetAgentTools so tests can assert dedup behavior across actions.
	calls []stubListerCall
}

type stubListerCall struct {
	agentID string
	userID  string
}

func (s *stubLister) GetAgentTools(agentID, userID string) ([]bridgeclient.BridgeToolInfo, error) {
	s.calls = append(s.calls, stubListerCall{agentID: agentID, userID: userID})
	return s.tools, s.err
}

func mmEmbeddedTool(name string) bridgeclient.BridgeToolInfo {
	return bridgeclient.BridgeToolInfo{Name: name, ServerOrigin: EmbeddedMattermostMCPOrigin}
}

// mmNamespacedEmbeddedTool builds an embedded Mattermost MCP tool as returned
// by current agents releases: a namespaced Name plus the legacy bare name in
// BareName.
func mmNamespacedEmbeddedTool(bareName string) bridgeclient.BridgeToolInfo {
	return bridgeclient.BridgeToolInfo{
		Name:         "mattermost__" + bareName,
		BareName:     bareName,
		ServerOrigin: EmbeddedMattermostMCPOrigin,
	}
}

func automationWithTools(tools []string, guardrails *model.Guardrails) *model.Automation {
	return &model.Automation{
		Actions: []model.Action{
			{
				ID: "ai1",
				AIPrompt: &model.AIPromptActionConfig{
					ProviderType: "agent",
					ProviderID:   "agent-1",
					AllowedTools: tools,
					Guardrails:   guardrails,
				},
			},
		},
	}
}

func TestValidateAllowedTools_GetUserChannels_AllowedWithoutGuardrails(t *testing.T) {
	bridge := &stubLister{tools: []bridgeclient.BridgeToolInfo{mmEmbeddedTool("get_user_channels")}}
	f := automationWithTools([]string{"get_user_channels"}, nil)

	require.NoError(t, ValidateAllowedTools(f, "user1", bridge))
}

func TestValidateAllowedTools_GetUserChannels_AllowedWithEmptyGuardrails(t *testing.T) {
	bridge := &stubLister{tools: []bridgeclient.BridgeToolInfo{mmEmbeddedTool("get_user_channels")}}
	f := automationWithTools([]string{"get_user_channels"}, &model.Guardrails{Channels: nil})

	require.NoError(t, ValidateAllowedTools(f, "user1", bridge))
}

func TestValidateAllowedTools_GetUserChannels_RejectedWithGuardrails(t *testing.T) {
	bridge := &stubLister{tools: []bridgeclient.BridgeToolInfo{mmEmbeddedTool("get_user_channels")}}
	guardrails := &model.Guardrails{Channels: []model.GuardrailChannel{
		{ChannelID: mmmodel.NewId(), TeamID: mmmodel.NewId()},
	}}
	f := automationWithTools([]string{"get_user_channels"}, guardrails)

	err := ValidateAllowedTools(f, "user1", bridge)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "get_user_channels is not permitted when channel guardrails are configured")
}

// TestValidateAllowedTools_NamespacedName_StoredNamespaced accepts a stored
// allowlist entry that matches the namespaced BridgeToolInfo.Name.
func TestValidateAllowedTools_NamespacedName_StoredNamespaced(t *testing.T) {
	bridge := &stubLister{tools: []bridgeclient.BridgeToolInfo{mmNamespacedEmbeddedTool("search_posts")}}
	f := automationWithTools([]string{"mattermost__search_posts"}, nil)

	require.NoError(t, ValidateAllowedTools(f, "user1", bridge))
}

// TestValidateAllowedTools_NamespacedName_StoredBare accepts a stored allowlist
// entry that matches the legacy BridgeToolInfo.BareName even when the bridge
// reports the namespaced Name.
func TestValidateAllowedTools_NamespacedName_StoredBare(t *testing.T) {
	bridge := &stubLister{tools: []bridgeclient.BridgeToolInfo{mmNamespacedEmbeddedTool("search_posts")}}
	f := automationWithTools([]string{"search_posts"}, nil)

	require.NoError(t, ValidateAllowedTools(f, "user1", bridge))
}

// TestValidateAllowedTools_NamespacedName_GuardrailRejection enforces the
// get_user_channels guardrail rule when the stored entry is namespaced.
func TestValidateAllowedTools_NamespacedName_GuardrailRejection(t *testing.T) {
	bridge := &stubLister{tools: []bridgeclient.BridgeToolInfo{mmNamespacedEmbeddedTool("get_user_channels")}}
	guardrails := &model.Guardrails{Channels: []model.GuardrailChannel{
		{ChannelID: mmmodel.NewId(), TeamID: mmmodel.NewId()},
	}}
	f := automationWithTools([]string{"mattermost__get_user_channels"}, guardrails)

	err := ValidateAllowedTools(f, "user1", bridge)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "get_user_channels is not permitted when channel guardrails are configured")
}

// TestValidateAllowedTools_NamespacedName_NotInCatalog rejects a namespaced
// embedded tool whose bare name is not in the supported catalog.
func TestValidateAllowedTools_NamespacedName_NotInCatalog(t *testing.T) {
	bridge := &stubLister{tools: []bridgeclient.BridgeToolInfo{mmNamespacedEmbeddedTool("unknown_tool")}}
	f := automationWithTools([]string{"mattermost__unknown_tool"}, nil)

	err := ValidateAllowedTools(f, "user1", bridge)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not in the supported catalog")
}

// TestValidateAllowedTools_AgentNoTools_VerifiesAccess covers the gap fixed
// by this change: an ai_prompt agent action with empty allowed_tools must
// still trigger a bridge call so the creator's access to the agent is
// verified at save time. An empty (200) tools response means access OK.
func TestValidateAllowedTools_AgentNoTools_VerifiesAccess(t *testing.T) {
	bridge := &stubLister{tools: []bridgeclient.BridgeToolInfo{}}
	f := automationWithTools(nil, nil)

	require.NoError(t, ValidateAllowedTools(f, "user1", bridge))
	require.Len(t, bridge.calls, 1)
	assert.Equal(t, "agent-1", bridge.calls[0].agentID)
	assert.Equal(t, "user1", bridge.calls[0].userID)
}

// TestValidateAllowedTools_AgentEmptyAllowedTools_AccessDenied surfaces a
// bridge denial as ErrToolDiscovery so callers map it to a 5xx response.
func TestValidateAllowedTools_AgentEmptyAllowedTools_AccessDenied(t *testing.T) {
	bridge := &stubLister{err: fmt.Errorf("request failed with status 403: permission denied")}
	f := automationWithTools(nil, nil)

	err := ValidateAllowedTools(f, "user1", bridge)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrToolDiscovery))
	assert.Contains(t, err.Error(), `failed to list tools for agent "agent-1"`)
	assert.Contains(t, err.Error(), "status 403")
}

// TestValidateAllowedTools_BridgeNil_AgentEmptyAllowedTools rejects when the
// agents plugin (and therefore the bridge) is unavailable but the automation has
// an ai_prompt agent action. Mirrors the pre-existing nil-bridge behavior
// when allowed_tools was set.
func TestValidateAllowedTools_BridgeNil_AgentEmptyAllowedTools(t *testing.T) {
	f := automationWithTools(nil, nil)

	err := ValidateAllowedTools(f, "user1", nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrToolDiscovery))
	assert.Contains(t, err.Error(), "bridge client unavailable")
}

// TestValidateAllowedTools_AgentEmptyAllowedTools_MissingProviderID rejects a
// misconfigured action up front rather than calling the bridge with an empty
// agent ID.
func TestValidateAllowedTools_AgentEmptyAllowedTools_MissingProviderID(t *testing.T) {
	f := &model.Automation{
		Actions: []model.Action{{
			ID: "ai1",
			AIPrompt: &model.AIPromptActionConfig{
				ProviderType: "agent",
				ProviderID:   "",
			},
		}},
	}
	bridge := &stubLister{}

	err := ValidateAllowedTools(f, "user1", bridge)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires provider_id")
	assert.Empty(t, bridge.calls, "bridge must not be called for an action missing provider_id")
}

// TestValidateAllowedTools_ServiceProvider_NoBridgeCall confirms service
// providers are out of scope for the bridge access check (they don't go
// through the agent ACL).
func TestValidateAllowedTools_ServiceProvider_NoBridgeCall(t *testing.T) {
	bridge := &stubLister{}
	f := &model.Automation{
		Actions: []model.Action{{
			ID: "svc",
			AIPrompt: &model.AIPromptActionConfig{
				ProviderType: "service",
				ProviderID:   "openai",
			},
		}},
	}

	require.NoError(t, ValidateAllowedTools(f, "user1", bridge))
	assert.Empty(t, bridge.calls)
}

// TestValidateAllowedTools_DedupesAcrossActions ensures an automation with multiple
// ai_prompt actions sharing one agent triggers exactly one bridge call.
// This is the property that prevents the access check from being made twice
// when one action has allowed_tools set and another does not.
func TestValidateAllowedTools_DedupesAcrossActions(t *testing.T) {
	bridge := &stubLister{tools: []bridgeclient.BridgeToolInfo{
		{Name: "search", ServerOrigin: "external-mcp"},
	}}
	f := &model.Automation{
		Actions: []model.Action{
			{
				ID: "with-tools",
				AIPrompt: &model.AIPromptActionConfig{
					ProviderType: "agent",
					ProviderID:   "shared-agent",
					AllowedTools: []string{"search"},
				},
			},
			{
				ID: "without-tools",
				AIPrompt: &model.AIPromptActionConfig{
					ProviderType: "agent",
					ProviderID:   "shared-agent",
				},
			},
		},
	}

	require.NoError(t, ValidateAllowedTools(f, "user1", bridge))
	require.Len(t, bridge.calls, 1, "bridge must be called once per unique agent across both actions")
	assert.Equal(t, "shared-agent", bridge.calls[0].agentID)
}

// TestValidateAllowedTools_DistinctAgents_OneCallPerAgent confirms the cache
// is keyed on agentID, not on the (already-deduped) provider_id seen so far.
func TestValidateAllowedTools_DistinctAgents_OneCallPerAgent(t *testing.T) {
	bridge := &stubLister{tools: []bridgeclient.BridgeToolInfo{}}
	f := &model.Automation{
		Actions: []model.Action{
			{
				ID: "a",
				AIPrompt: &model.AIPromptActionConfig{
					ProviderType: "agent", ProviderID: "agent-a",
				},
			},
			{
				ID: "b",
				AIPrompt: &model.AIPromptActionConfig{
					ProviderType: "agent", ProviderID: "agent-b",
				},
			},
		},
	}

	require.NoError(t, ValidateAllowedTools(f, "user1", bridge))
	require.Len(t, bridge.calls, 2)
	gotAgents := []string{bridge.calls[0].agentID, bridge.calls[1].agentID}
	assert.ElementsMatch(t, []string{"agent-a", "agent-b"}, gotAgents)
}

// TestValidateAllowedTools_AgentEmptyAllowedTools_MissingUserID surfaces a
// programmer-error precondition before any bridge round-trip.
func TestValidateAllowedTools_AgentEmptyAllowedTools_MissingUserID(t *testing.T) {
	bridge := &stubLister{}
	f := automationWithTools(nil, nil)

	err := ValidateAllowedTools(f, "", bridge)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing user id")
	assert.Empty(t, bridge.calls)
}

func TestValidateCreatorOnlyToolsForTriggerer(t *testing.T) {
	t.Run("rejects creator-only tool under triggerer", func(t *testing.T) {
		f := &model.Automation{
			Trigger: model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch1"}},
			Actions: []model.Action{{
				ID: "a1",
				AIPrompt: &model.AIPromptActionConfig{
					RequestAs:    "triggerer",
					AllowedTools: []string{"search_users"},
				},
			}},
		}
		err := ValidateCreatorOnlyToolsForTriggerer(f)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "requires request_as")
	})

	t.Run("rejects creator-only tool when request_as unset", func(t *testing.T) {
		f := &model.Automation{
			Trigger: model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch1"}},
			Actions: []model.Action{{
				ID: "a1",
				AIPrompt: &model.AIPromptActionConfig{
					AllowedTools: []string{"add_user_to_channel"},
				},
			}},
		}
		require.Error(t, ValidateCreatorOnlyToolsForTriggerer(f))
	})

	t.Run("allows creator-only tool with request_as creator", func(t *testing.T) {
		f := &model.Automation{
			Trigger: model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch1"}},
			Actions: []model.Action{{
				ID: "a1",
				AIPrompt: &model.AIPromptActionConfig{
					RequestAs:    "creator",
					AllowedTools: []string{"search_users", "create_channel"},
				},
			}},
		}
		require.NoError(t, ValidateCreatorOnlyToolsForTriggerer(f))
	})

	t.Run("allows channel-scoped read tools under triggerer", func(t *testing.T) {
		f := &model.Automation{
			Trigger: model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch1"}},
			Actions: []model.Action{{
				ID: "a1",
				AIPrompt: &model.AIPromptActionConfig{
					RequestAs:    "triggerer",
					AllowedTools: []string{"search_posts", "read_channel"},
				},
			}},
		}
		require.NoError(t, ValidateCreatorOnlyToolsForTriggerer(f))
	})

	t.Run("namespaced creator-only tool is rejected", func(t *testing.T) {
		f := &model.Automation{
			Actions: []model.Action{{
				ID: "a1",
				AIPrompt: &model.AIPromptActionConfig{
					RequestAs:    "triggerer",
					AllowedTools: []string{"mattermost__list_agents"},
				},
			}},
		}
		err := ValidateCreatorOnlyToolsForTriggerer(f)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "list_agents")
	})

	t.Run("allows creator-only tool for schedule trigger under triggerer", func(t *testing.T) {
		// Schedule triggers have no triggering user, so request_as always
		// resolves to the creator and creator-only tools must be permitted.
		f := &model.Automation{
			Trigger: model.Trigger{Schedule: &model.ScheduleConfig{ChannelID: "ch1"}},
			Actions: []model.Action{{
				ID: "a1",
				AIPrompt: &model.AIPromptActionConfig{
					RequestAs:    "triggerer",
					AllowedTools: []string{"search_users"},
				},
			}},
		}
		require.NoError(t, ValidateCreatorOnlyToolsForTriggerer(f))
	})
}
