package automation

import (
	"errors"
	"testing"

	"github.com/mattermost/mattermost-plugin-agents/public/bridgeclient"
	mmmodel "github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin/plugintest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/mattermost/mattermost-plugin-channel-automation/server/automation/action"
	"github.com/mattermost/mattermost-plugin-channel-automation/server/model"
)

// mockBridgeClient implements action.BridgeClient for executor tests.
type mockBridgeClient struct {
	agentResponse string
	lastReq       bridgeclient.CompletionRequest
}

func (m *mockBridgeClient) AgentCompletion(agent string, req bridgeclient.CompletionRequest) (string, error) {
	m.lastReq = req
	return m.agentResponse, nil
}

func (m *mockBridgeClient) ServiceCompletion(_ string, req bridgeclient.CompletionRequest) (string, error) {
	m.lastReq = req
	return m.agentResponse, nil
}

func (m *mockBridgeClient) GetAgentTools(_, _ string) ([]bridgeclient.BridgeToolInfo, error) {
	return nil, nil
}

func TestExecutor_SingleAction(t *testing.T) {
	api := &plugintest.API{}
	api.On("HasPermissionToChannel", "creator1", "ch1", mmmodel.PermissionCreatePost).Return(true)
	api.On("CreatePost", mock.Anything).Return(&mmmodel.Post{
		Id:        "p1",
		ChannelId: "ch1",
		Message:   "Hello alice",
	}, nil)

	registry := NewRegistry()
	registry.RegisterAction(action.NewSendMessageAction(api, "bot"))

	executor := NewExecutor(registry)

	f := &model.Automation{
		ID:        "auto1",
		Name:      "Test",
		CreatedBy: "creator1",
		Actions: []model.Action{
			{ID: "act1", SendMessage: &model.SendMessageActionConfig{ChannelID: "ch1", Body: "Hello {{.Trigger.User.Username}}"}},
		},
	}
	triggerData := model.TriggerData{
		Post:    &model.SafePost{Id: "post1", ChannelId: "ch1"},
		Channel: &model.SafeChannel{Id: "ch1"},
		User:    &model.SafeUser{Id: "user1", Username: "alice"},
	}

	ctx, err := executor.Execute(f, triggerData)
	require.NoError(t, err)
	require.NotNil(t, ctx)
	assert.Equal(t, "auto1", ctx.AutomationID)
	api.AssertCalled(t, "CreatePost", mock.Anything)
}

func TestExecutor_MultiAction_CumulativeContext(t *testing.T) {
	api := &plugintest.API{}
	api.On("HasPermissionToChannel", "creator1", mock.Anything, mmmodel.PermissionCreatePost).Return(true)
	api.On("CreatePost", mock.Anything).Return(&mmmodel.Post{
		Id:        "p1",
		ChannelId: "ch2",
		Message:   "msg1",
	}, nil).Once()
	api.On("CreatePost", mock.Anything).Return(&mmmodel.Post{
		Id:        "p2",
		ChannelId: "ch3",
		Message:   "msg2",
	}, nil).Once()

	registry := NewRegistry()
	registry.RegisterAction(action.NewSendMessageAction(api, "bot"))

	executor := NewExecutor(registry)

	f := &model.Automation{
		ID:        "auto1",
		Name:      "Test",
		CreatedBy: "creator1",
		Actions: []model.Action{
			{ID: "act1", SendMessage: &model.SendMessageActionConfig{ChannelID: "ch2", Body: "msg1"}},
			{ID: "act2", SendMessage: &model.SendMessageActionConfig{ChannelID: "ch3", Body: "msg2"}},
		},
	}
	triggerData := model.TriggerData{
		Post: &model.SafePost{Id: "post1", ChannelId: "ch1"},
		User: &model.SafeUser{Id: "user1", Username: "alice"},
	}

	_, err := executor.Execute(f, triggerData)
	require.NoError(t, err)
	api.AssertNumberOfCalls(t, "CreatePost", 2)
}

func TestExecutor_FirstFailureStops(t *testing.T) {
	api := &plugintest.API{}
	api.On("HasPermissionToChannel", "creator1", mock.Anything, mmmodel.PermissionCreatePost).Return(true)
	api.On("CreatePost", mock.Anything).Return(nil, mmmodel.NewAppError("CreatePost", "error", nil, "", 500)).Once()

	registry := NewRegistry()
	registry.RegisterAction(action.NewSendMessageAction(api, "bot"))

	executor := NewExecutor(registry)

	f := &model.Automation{
		ID:        "auto1",
		Name:      "Test",
		CreatedBy: "creator1",
		Actions: []model.Action{
			{ID: "act1", SendMessage: &model.SendMessageActionConfig{ChannelID: "ch2", Body: "msg1"}},
			{ID: "act2", SendMessage: &model.SendMessageActionConfig{ChannelID: "ch3", Body: "msg2"}},
		},
	}
	triggerData := model.TriggerData{
		Post: &model.SafePost{Id: "post1", ChannelId: "ch1"},
		User: &model.SafeUser{Id: "user1", Username: "alice"},
	}

	_, err := executor.Execute(f, triggerData)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `action "act1" failed`)
	var actionErr *ActionError
	require.True(t, errors.As(err, &actionErr), "expected *ActionError, got %T", err)
	assert.Equal(t, "act1", actionErr.ActionID)
	assert.Equal(t, "send_message", actionErr.ActionType)
	// Second action should never be called.
	api.AssertNumberOfCalls(t, "CreatePost", 1)
}

func TestExecutor_ChainedAIPromptThenSendMessage(t *testing.T) {
	api := &plugintest.API{}
	api.On("HasPermissionToChannel", "creator1", "ch1", mmmodel.PermissionCreatePost).Return(true)
	api.On("CreatePost", mock.MatchedBy(func(p *mmmodel.Post) bool {
		return p.Message == "AI said: hello from AI"
	})).Return(&mmmodel.Post{
		Id:        "p1",
		ChannelId: "ch1",
		Message:   "AI said: hello from AI",
	}, nil)
	api.On("LogDebug", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	api.On("LogDebug", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	api.On("GetUser", mock.Anything).Return(&mmmodel.User{Id: "creator1", IsBot: false}, nil)

	// Mock bridge client for AI action
	bc := &mockBridgeClient{agentResponse: "hello from AI"}

	registry := NewRegistry()
	registry.RegisterAction(action.NewSendMessageAction(api, "bot"))
	registry.RegisterAction(action.NewAIPromptAction(api, bc, ""))

	executor := NewExecutor(registry)

	f := &model.Automation{
		ID:        "auto1",
		Name:      "Chained AI Test",
		CreatedBy: "creator1",
		Actions: []model.Action{
			{
				ID: "ai_step",
				AIPrompt: &model.AIPromptActionConfig{
					Prompt:       "Summarize: {{.Trigger.Post.Message}}",
					ProviderType: "agent",
					ProviderID:   "ai-bot",
				},
			},
			{
				ID: "send_step",
				SendMessage: &model.SendMessageActionConfig{
					ChannelID: "ch1",
					Body:      `AI said: {{(index .Steps "ai_step").Message}}`,
				},
			},
		},
	}
	triggerData := model.TriggerData{
		Post:    &model.SafePost{Id: "post1", ChannelId: "ch1", Message: "some text"},
		Channel: &model.SafeChannel{Id: "ch1"},
		User:    &model.SafeUser{Id: "user1", Username: "alice"},
	}

	_, err := executor.Execute(f, triggerData)
	require.NoError(t, err)
	api.AssertCalled(t, "CreatePost", mock.Anything)
	// Posts: [trigger metadata (system), user-generated content (user), user prompt (user)]
	require.Len(t, bc.lastReq.Posts, 3)
	assert.Contains(t, bc.lastReq.Posts[0].Message, "<trigger_context>")
	assert.NotContains(t, bc.lastReq.Posts[0].Message, "some text") // user content not in system prompt
	assert.Contains(t, bc.lastReq.Posts[1].Message, "some text")
	assert.Contains(t, bc.lastReq.Posts[1].Message, "<user_data>")
	assert.Equal(t, "Summarize: some text", bc.lastReq.Posts[2].Message)
}

func TestExecutor_ChannelGuardrail_BlocksDifferentChannel(t *testing.T) {
	api := &plugintest.API{}

	registry := NewRegistry()
	registry.RegisterAction(action.NewSendMessageAction(api, "bot"))

	executor := NewExecutor(registry)

	f := &model.Automation{
		ID:        "auto1",
		Name:      "Test",
		CreatedBy: "creator1",
		Actions: []model.Action{
			{ID: "act1", SendMessage: &model.SendMessageActionConfig{ChannelID: "ch-other", Body: "hello"}},
		},
	}
	triggerData := model.TriggerData{
		Post:    &model.SafePost{Id: "post1", ChannelId: "ch1"},
		Channel: &model.SafeChannel{Id: "ch1"},
		User:    &model.SafeUser{Id: "user1", Username: "alice"},
	}

	_, err := executor.Execute(f, triggerData)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "restricted to the triggering channel")
	api.AssertNotCalled(t, "CreatePost", mock.Anything)
}

func TestExecutor_UnknownActionType(t *testing.T) {
	registry := NewRegistry()
	executor := NewExecutor(registry)

	f := &model.Automation{
		ID:   "auto1",
		Name: "Test",
		Actions: []model.Action{
			{ID: "act1"},
		},
	}
	triggerData := model.TriggerData{
		Post: &model.SafePost{Id: "post1", ChannelId: "ch1"},
	}

	_, err := executor.Execute(f, triggerData)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown action type")
}

func TestExecutor_PermissionDenied(t *testing.T) {
	api := &plugintest.API{}
	api.On("HasPermissionToChannel", "creator1", "ch2", mmmodel.PermissionCreatePost).Return(false)

	registry := NewRegistry()
	registry.RegisterAction(action.NewSendMessageAction(api, "bot"))

	executor := NewExecutor(registry)

	f := &model.Automation{
		ID:        "auto1",
		Name:      "Test",
		CreatedBy: "creator1",
		Actions: []model.Action{
			{ID: "act1", SendMessage: &model.SendMessageActionConfig{ChannelID: "ch2", Body: "msg"}},
		},
	}
	triggerData := model.TriggerData{
		Post: &model.SafePost{Id: "post1", ChannelId: "ch1"},
		User: &model.SafeUser{Id: "user1", Username: "alice"},
	}

	_, err := executor.Execute(f, triggerData)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not have permission to post in channel")
	api.AssertNotCalled(t, "CreatePost", mock.Anything)
}
