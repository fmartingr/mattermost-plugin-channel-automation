package automation

import (
	"testing"

	"github.com/mattermost/mattermost-plugin-agents/public/bridgeclient"
	mmmodel "github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin/plugintest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/mattermost/mattermost-plugin-channel-automation/server/automation/action"
	"github.com/mattermost/mattermost-plugin-channel-automation/server/automation/hooks"
	"github.com/mattermost/mattermost-plugin-channel-automation/server/model"
)

// embeddedTool builds a discovered embedded Mattermost MCP tool. When bareName
// differs from name the tool is reported in its namespaced form.
func embeddedTool(name, bareName string) bridgeclient.BridgeToolInfo {
	return bridgeclient.BridgeToolInfo{
		Name:         name,
		BareName:     bareName,
		ServerOrigin: hooks.EmbeddedMattermostMCPOrigin,
	}
}

// TestTriggeredAutomation_BridgeRequestIncludesBeforeHooksForBothToolForms
// drives the real trigger path (Dispatcher matches the automation and builds
// trigger data; Executor runs the ai_prompt action exactly as the worker does)
// and asserts the resulting bridge CompletionRequest carries before-hook
// callbacks for both a bare and a namespaced allowed_tools entry.
func TestTriggeredAutomation_BridgeRequestIncludesBeforeHooksForBothToolForms(t *testing.T) {
	const (
		channelID = "ch1"
		teamID    = "team1"
	)

	store, _ := setupStore(t)
	registry := newTestRegistry()

	bc := &mockBridgeClient{
		agentResponse: "ok",
		agentTools: []bridgeclient.BridgeToolInfo{
			embeddedTool("search_posts", ""),
			embeddedTool("mattermost__add_user_to_channel", "add_user_to_channel"),
		},
	}

	api := &plugintest.API{}
	api.On("GetChannel", channelID).Return(&mmmodel.Channel{Id: channelID, Name: "n", TeamId: teamID}, (*mmmodel.AppError)(nil)).Maybe()
	api.On("GetUser", "u1").Return(&mmmodel.User{Id: "u1", Username: "alice"}, (*mmmodel.AppError)(nil)).Maybe()
	api.On("PublishUserTyping", mock.Anything, mock.Anything, mock.Anything).Return((*mmmodel.AppError)(nil)).Maybe()
	for _, n := range []int{1, 3, 5, 7, 9, 11, 13, 15} {
		args := make([]any, n)
		for i := range args {
			args[i] = mock.Anything
		}
		api.On("LogDebug", args...).Return().Maybe()
	}

	registry.RegisterAction(action.NewAIPromptAction(api, bc, "bot"))

	triggerSvc := NewTriggerService(store, registry)
	enqueuer := &fakeEnqueuer{}
	notifier := &fakeNotifier{}
	dispatcher := NewDispatcher(api, triggerSvc, enqueuer, notifier)
	executor := NewExecutor(registry)

	auto := &model.Automation{
		ID:        "auto1",
		Name:      "test",
		Enabled:   true,
		CreatedBy: "creator1",
		Trigger:   model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: channelID}},
		Actions: []model.Action{{
			ID: "ai-step",
			AIPrompt: &model.AIPromptActionConfig{
				Prompt:       "q",
				ProviderType: model.AIProviderTypeAgent,
				ProviderID:   "bot1",
				AllowedTools: []string{"search_posts", "mattermost__add_user_to_channel"},
				Guardrails: &model.Guardrails{Channels: []model.GuardrailChannel{
					{ChannelID: channelID, TeamID: teamID},
				}},
			},
		}},
	}
	require.NoError(t, store.Save(auto))

	// Fire the trigger. The dispatcher matches the automation via the channel
	// index and builds the trigger data the executor will run against.
	dispatcher.Dispatch(&model.Event{
		Type: model.TriggerTypeMessagePosted,
		Post: &mmmodel.Post{Id: "p1", ChannelId: channelID, UserId: "u1", Message: "hi"},
	})
	require.Len(t, enqueuer.items, 1)

	// Run the enqueued work item through the executor, mirroring the worker.
	saved, err := store.Get(auto.ID)
	require.NoError(t, err)
	_, err = executor.Execute(saved, enqueuer.items[0].TriggerData)
	require.NoError(t, err)

	// The allowed_tools are forwarded verbatim (both bare and namespaced forms).
	assert.Equal(t, []string{"search_posts", "mattermost__add_user_to_channel"}, bc.lastReq.AllowedTools)

	// Each allowed tool also carries its before-hook callback.
	wantCallback := "/api/v1/hooks/tools/auto1/ai-step/before"
	require.Len(t, bc.lastReq.ToolHooks, 2)
	assert.Equal(t, wantCallback, bc.lastReq.ToolHooks["search_posts"].BeforeCallback)
	assert.Equal(t, wantCallback, bc.lastReq.ToolHooks["mattermost__add_user_to_channel"].BeforeCallback)
}
