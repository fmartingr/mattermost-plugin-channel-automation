package action

import (
	"fmt"
	"strings"
	"testing"
	"time"

	mmmodel "github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin/plugintest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/mattermost/mattermost-plugin-agents/public/bridgeclient"

	"github.com/mattermost/mattermost-plugin-channel-automation/server/model"
)

// fixedTime is a stable timestamp used in buildTriggerContext tests.
var fixedTime = time.Date(2026, time.April, 22, 14, 30, 45, 0, time.UTC)

// mockBridgeClient implements BridgeClient for testing.
type mockBridgeClient struct {
	agentResponse   string
	agentErr        error
	serviceResponse string
	serviceErr      error

	agentTools    []bridgeclient.BridgeToolInfo
	agentToolsErr error

	// agentBlock, when non-nil, makes AgentCompletion block on receive from
	// the channel before returning. Tests can use this to keep the LLM call
	// in flight while asserting typing-indicator behavior, then close the
	// channel to release it.
	agentBlock <-chan struct{}
	// agentStarted, when non-nil, is closed when AgentCompletion is entered.
	// Tests can use this to synchronize with the in-flight call.
	agentStarted chan<- struct{}

	lastAgent   string
	lastService string
	lastReq     bridgeclient.CompletionRequest
}

func (m *mockBridgeClient) AgentCompletion(agent string, req bridgeclient.CompletionRequest) (string, error) {
	m.lastAgent = agent
	m.lastReq = req
	if m.agentStarted != nil {
		close(m.agentStarted)
		m.agentStarted = nil
	}
	if m.agentBlock != nil {
		<-m.agentBlock
	}
	return m.agentResponse, m.agentErr
}

func (m *mockBridgeClient) ServiceCompletion(service string, req bridgeclient.CompletionRequest) (string, error) {
	m.lastService = service
	m.lastReq = req
	return m.serviceResponse, m.serviceErr
}

func (m *mockBridgeClient) GetAgentTools(_, _ string) ([]bridgeclient.BridgeToolInfo, error) {
	return m.agentTools, m.agentToolsErr
}

func newTestAPI() *plugintest.API {
	api := &plugintest.API{}
	api.On("LogDebug", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	api.On("LogDebug", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	// Default: any resolved ai_prompt user is a non-bot human.
	api.On("GetUser", mock.Anything).Return(&mmmodel.User{Id: "user1", Username: "user1", IsBot: false}, nil).Maybe()
	return api
}

func TestAIPromptAction_Type(t *testing.T) {
	a := NewAIPromptAction(nil, nil, "")
	assert.Equal(t, "ai_prompt", a.Type())
}

func TestAIPromptAction_Execute_AgentSuccess(t *testing.T) {
	api := newTestAPI()
	bc := &mockBridgeClient{agentResponse: "AI says hello"}
	a := NewAIPromptAction(api, bc, "")

	act := &model.Action{
		ID: "ai1",
		AIPrompt: &model.AIPromptActionConfig{
			Prompt:       "Summarize: {{.Trigger.Post.Message}}",
			ProviderType: "agent",
			ProviderID:   "ai-bot",
		},
	}
	ctx := &model.AutomationContext{
		Trigger: model.TriggerData{
			Post: &model.SafePost{Message: "Hello world"},
		},
		Steps: make(map[string]model.StepOutput),
	}

	output, err := a.Execute(act, ctx)
	require.NoError(t, err)
	require.NotNil(t, output)
	assert.Equal(t, "AI says hello", output.Message)
	assert.Equal(t, "ai-bot", bc.lastAgent)
	// Posts: [trigger metadata (system), user-generated post content (user), user prompt (user)]
	require.Len(t, bc.lastReq.Posts, 3)
	assert.Equal(t, "system", bc.lastReq.Posts[0].Role)
	assert.Contains(t, bc.lastReq.Posts[0].Message, "<trigger_context>")
	assert.NotContains(t, bc.lastReq.Posts[0].Message, "Hello world") // post message must NOT be in system prompt
	assert.Equal(t, "user", bc.lastReq.Posts[1].Role)
	assert.Contains(t, bc.lastReq.Posts[1].Message, "Hello world")
	assert.Contains(t, bc.lastReq.Posts[1].Message, "<user_data>")
	assert.Equal(t, "user", bc.lastReq.Posts[2].Role)
	assert.Equal(t, "Summarize: Hello world", bc.lastReq.Posts[2].Message)
}

func TestAIPromptAction_Execute_ForwardsTriggerPostFileIDs(t *testing.T) {
	api := newTestAPI()
	bc := &mockBridgeClient{agentResponse: "AI saw the screenshot"}
	a := NewAIPromptAction(api, bc, "")

	act := &model.Action{
		ID: "ai1",
		AIPrompt: &model.AIPromptActionConfig{
			Prompt:       "Summarize the attached screenshot",
			ProviderType: "agent",
			ProviderID:   "ai-bot",
		},
	}
	ctx := &model.AutomationContext{
		Trigger: model.TriggerData{
			Post: &model.SafePost{Message: "Here is the bug", FileIds: []string{"file1", "file2"}},
		},
		Steps: make(map[string]model.StepOutput),
	}

	output, err := a.Execute(act, ctx)
	require.NoError(t, err)
	require.NotNil(t, output)
	require.Len(t, bc.lastReq.Posts, 3)
	assert.Equal(t, []string{"file1", "file2"}, bc.lastReq.Posts[1].FileIDs)
	assert.Empty(t, bc.lastReq.Posts[2].FileIDs)
}

func TestAIPromptAction_Execute_ForwardsFileIDsForImageOnlyPost(t *testing.T) {
	api := newTestAPI()
	bc := &mockBridgeClient{agentResponse: "AI saw the screenshot"}
	a := NewAIPromptAction(api, bc, "")

	act := &model.Action{
		ID: "ai1",
		AIPrompt: &model.AIPromptActionConfig{
			Prompt:       "Describe the attached screenshot",
			ProviderType: "agent",
			ProviderID:   "ai-bot",
		},
	}
	ctx := &model.AutomationContext{
		Trigger: model.TriggerData{
			Post: &model.SafePost{Id: "post1", FileIds: []string{"image-file-id"}},
		},
		Steps: make(map[string]model.StepOutput),
	}

	output, err := a.Execute(act, ctx)
	require.NoError(t, err)
	require.NotNil(t, output)
	require.Len(t, bc.lastReq.Posts, 3)
	assert.Equal(t, "user", bc.lastReq.Posts[1].Role)
	assert.Empty(t, bc.lastReq.Posts[1].Message)
	assert.Equal(t, []string{"image-file-id"}, bc.lastReq.Posts[1].FileIDs)
	assert.Equal(t, "Describe the attached screenshot", bc.lastReq.Posts[2].Message)
	assert.Empty(t, bc.lastReq.Posts[2].FileIDs)
}

func TestAIPromptAction_Execute_ServiceSuccess(t *testing.T) {
	api := newTestAPI()
	bc := &mockBridgeClient{serviceResponse: "Service response"}
	a := NewAIPromptAction(api, bc, "")

	act := &model.Action{
		ID: "ai1",
		AIPrompt: &model.AIPromptActionConfig{
			Prompt:       "Tell me about {{.Trigger.User.Username}}",
			ProviderType: "service",
			ProviderID:   "openai",
		},
	}
	ctx := &model.AutomationContext{
		Trigger: model.TriggerData{
			User: &model.SafeUser{Id: "user1", Username: "alice"},
		},
		Steps: make(map[string]model.StepOutput),
	}

	output, err := a.Execute(act, ctx)
	require.NoError(t, err)
	require.NotNil(t, output)
	assert.Equal(t, "Service response", output.Message)
	assert.Equal(t, "openai", bc.lastService)
	// Posts: [trigger metadata (system), user-generated content (user), user prompt (user)]
	require.Len(t, bc.lastReq.Posts, 3)
	assert.Equal(t, "system", bc.lastReq.Posts[0].Role)
	assert.Contains(t, bc.lastReq.Posts[0].Message, "Triggering User ID: user1")
	assert.NotContains(t, bc.lastReq.Posts[0].Message, "alice")
	assert.Equal(t, "user", bc.lastReq.Posts[1].Role)
	assert.Contains(t, bc.lastReq.Posts[1].Message, "Triggering Username: alice")
	assert.Equal(t, "user", bc.lastReq.Posts[2].Role)
	assert.Equal(t, "Tell me about alice", bc.lastReq.Posts[2].Message)
}

func TestAIPromptAction_Execute_TemplateWithStepOutputs(t *testing.T) {
	api := newTestAPI()
	bc := &mockBridgeClient{agentResponse: "refined output"}
	a := NewAIPromptAction(api, bc, "")

	act := &model.Action{
		ID: "ai2",
		AIPrompt: &model.AIPromptActionConfig{
			Prompt:       `Refine: {{(index .Steps "step1").Message}}`,
			ProviderType: "agent",
			ProviderID:   "ai-bot",
		},
	}
	ctx := &model.AutomationContext{
		Trigger: model.TriggerData{},
		Steps: map[string]model.StepOutput{
			"step1": {Message: "previous result"},
		},
	}

	output, err := a.Execute(act, ctx)
	require.NoError(t, err)
	require.NotNil(t, output)
	assert.Equal(t, "refined output", output.Message)
	// Posts: [scope instruction (system), user prompt]
	require.Len(t, bc.lastReq.Posts, 2)
	assert.Equal(t, "system", bc.lastReq.Posts[0].Role)
	assert.Contains(t, bc.lastReq.Posts[0].Message, "Complete only the specific task")
	assert.Equal(t, "Refine: previous result", bc.lastReq.Posts[1].Message)
}

func TestAIPromptAction_Execute_MissingConfig(t *testing.T) {
	api := newTestAPI()
	bc := &mockBridgeClient{}
	a := NewAIPromptAction(api, bc, "")

	tests := []struct {
		name     string
		aiPrompt *model.AIPromptActionConfig
		errMsg   string
	}{
		{
			name:     "missing prompt",
			aiPrompt: &model.AIPromptActionConfig{ProviderType: "agent", ProviderID: "bot"},
			errMsg:   `missing required config key "prompt"`,
		},
		{
			name:     "missing provider_type",
			aiPrompt: &model.AIPromptActionConfig{Prompt: "hello", ProviderID: "bot"},
			errMsg:   `missing required config key "provider_type"`,
		},
		{
			name:     "missing provider_id",
			aiPrompt: &model.AIPromptActionConfig{Prompt: "hello", ProviderType: "agent"},
			errMsg:   `missing required config key "provider_id"`,
		},
		{
			name:     "nil config",
			aiPrompt: nil,
			errMsg:   `ai_prompt action has no ai_prompt config`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			act := &model.Action{ID: "ai1", AIPrompt: tc.aiPrompt}
			ctx := &model.AutomationContext{Trigger: model.TriggerData{}, Steps: make(map[string]model.StepOutput)}

			output, err := a.Execute(act, ctx)
			require.Error(t, err)
			assert.Nil(t, output)
			assert.Contains(t, err.Error(), tc.errMsg)
		})
	}
}

func TestAIPromptAction_Execute_NilBridgeClient(t *testing.T) {
	api := newTestAPI()
	a := NewAIPromptAction(api, nil, "")

	act := &model.Action{
		ID: "ai1",
		AIPrompt: &model.AIPromptActionConfig{
			Prompt:       "hello",
			ProviderType: "agent",
			ProviderID:   "bot",
		},
	}
	ctx := &model.AutomationContext{Trigger: model.TriggerData{}, Steps: make(map[string]model.StepOutput)}

	output, err := a.Execute(act, ctx)
	require.Error(t, err)
	assert.Nil(t, output)
	assert.Contains(t, err.Error(), "agents plugin is not installed or active")
}

func TestAIPromptAction_Execute_BridgeClientError(t *testing.T) {
	api := newTestAPI()
	bc := &mockBridgeClient{agentErr: fmt.Errorf("connection refused")}
	a := NewAIPromptAction(api, bc, "")

	act := &model.Action{
		ID: "ai1",
		AIPrompt: &model.AIPromptActionConfig{
			Prompt:       "hello",
			ProviderType: "agent",
			ProviderID:   "bot",
		},
	}
	ctx := &model.AutomationContext{Trigger: model.TriggerData{}, Steps: make(map[string]model.StepOutput)}

	output, err := a.Execute(act, ctx)
	require.Error(t, err)
	assert.Nil(t, output)
	assert.Contains(t, err.Error(), "AI completion failed")
	assert.Contains(t, err.Error(), "connection refused")
}

func TestAIPromptAction_Execute_BadTemplate(t *testing.T) {
	api := newTestAPI()
	bc := &mockBridgeClient{}
	a := NewAIPromptAction(api, bc, "")

	act := &model.Action{
		ID: "ai1",
		AIPrompt: &model.AIPromptActionConfig{
			Prompt:       "{{.Invalid",
			ProviderType: "agent",
			ProviderID:   "bot",
		},
	}
	ctx := &model.AutomationContext{Trigger: model.TriggerData{}, Steps: make(map[string]model.StepOutput)}

	output, err := a.Execute(act, ctx)
	require.Error(t, err)
	assert.Nil(t, output)
	assert.Contains(t, err.Error(), "failed to render template")
}

// mmTool builds a BridgeToolInfo for an embedded Mattermost MCP server tool.
func mmTool(name string) bridgeclient.BridgeToolInfo {
	return bridgeclient.BridgeToolInfo{Name: name, ServerOrigin: "embedded://mattermost"}
}

// mmNamespacedTool builds an embedded Mattermost MCP tool with the namespaced
// Name plus the legacy bare name, as returned by current agents releases.
func mmNamespacedTool(bareName string) bridgeclient.BridgeToolInfo {
	return bridgeclient.BridgeToolInfo{
		Name:         "mattermost__" + bareName,
		BareName:     bareName,
		ServerOrigin: "embedded://mattermost",
	}
}

func TestAIPromptAction_Execute_ToolHooksGuardrails(t *testing.T) {
	api := newTestAPI()
	bc := &mockBridgeClient{
		agentResponse: "ok",
		agentTools: []bridgeclient.BridgeToolInfo{
			mmTool("search_posts"),
			mmTool("add_user_to_channel"),
			{Name: "external_search"}, // non-MM tool, no hooks
		},
	}
	a := NewAIPromptAction(api, bc, "")

	chID := mmmodel.NewId()
	act := &model.Action{
		ID: "ai-step",
		AIPrompt: &model.AIPromptActionConfig{
			Prompt:       "q",
			ProviderType: "agent",
			ProviderID:   "ai-bot",
			RequestAs:    "creator",
			AllowedTools: []string{"search_posts", "add_user_to_channel", "external_search"},
			Guardrails: &model.Guardrails{Channels: []model.GuardrailChannel{{
				ChannelID: chID,
				TeamID:    mmmodel.NewId(),
			}}},
		},
	}
	ctx := &model.AutomationContext{
		AutomationID: "automation-99",
		CreatedBy:    "creator1",
		Trigger:      model.TriggerData{},
		Steps:        make(map[string]model.StepOutput),
	}

	_, err := a.Execute(act, ctx)
	require.NoError(t, err)
	require.NotNil(t, bc.lastReq.ToolHooks)
	// Only Mattermost MCP tools with at least one hook should be wired.
	require.Len(t, bc.lastReq.ToolHooks, 2)

	sp := bc.lastReq.ToolHooks["search_posts"]
	assert.Equal(t, "/api/v1/hooks/tools/automation-99/ai-step/before", sp.BeforeCallback)

	auc := bc.lastReq.ToolHooks["add_user_to_channel"]
	assert.Equal(t, "/api/v1/hooks/tools/automation-99/ai-step/before", auc.BeforeCallback)

	_, hasExternal := bc.lastReq.ToolHooks["external_search"]
	assert.False(t, hasExternal, "non-Mattermost MCP tools must not get hook callbacks")
}

// TestAIPromptAction_Execute_ToolHooksGuardrailsNamespaced confirms before-hooks
// are still wired when the stored allowlist uses namespaced tool names. The hook
// map keeps the stored (namespaced) form since the bridge accepts either.
func TestAIPromptAction_Execute_ToolHooksGuardrailsNamespaced(t *testing.T) {
	api := newTestAPI()
	// external__search_posts shares the bare name "search_posts" with the
	// Mattermost catalog but originates from an external MCP server. It must not
	// receive a guardrail hook — eligibility is gated on the bridge-resolved
	// ServerOrigin, not on stripping the namespace prefix.
	externalSearch := bridgeclient.BridgeToolInfo{
		Name:         "external__search_posts",
		BareName:     "search_posts",
		ServerOrigin: "https://external.example.com",
	}
	bc := &mockBridgeClient{
		agentResponse: "ok",
		agentTools: []bridgeclient.BridgeToolInfo{
			mmNamespacedTool("search_posts"),
			mmNamespacedTool("add_user_to_channel"),
			externalSearch,
		},
	}
	a := NewAIPromptAction(api, bc, "")

	act := &model.Action{
		ID: "ai-step",
		AIPrompt: &model.AIPromptActionConfig{
			Prompt:       "q",
			ProviderType: "agent",
			ProviderID:   "ai-bot",
			RequestAs:    "creator",
			AllowedTools: []string{"mattermost__search_posts", "mattermost__add_user_to_channel", "external__search_posts"},
			Guardrails: &model.Guardrails{Channels: []model.GuardrailChannel{{
				ChannelID: mmmodel.NewId(),
				TeamID:    mmmodel.NewId(),
			}}},
		},
	}
	ctx := &model.AutomationContext{
		AutomationID: "automation-99",
		CreatedBy:    "creator1",
		Trigger:      model.TriggerData{},
		Steps:        make(map[string]model.StepOutput),
	}

	_, err := a.Execute(act, ctx)
	require.NoError(t, err)
	require.Len(t, bc.lastReq.ToolHooks, 2)

	sp := bc.lastReq.ToolHooks["mattermost__search_posts"]
	assert.Equal(t, "/api/v1/hooks/tools/automation-99/ai-step/before", sp.BeforeCallback)

	auc := bc.lastReq.ToolHooks["mattermost__add_user_to_channel"]
	assert.Equal(t, "/api/v1/hooks/tools/automation-99/ai-step/before", auc.BeforeCallback)

	_, hasExternal := bc.lastReq.ToolHooks["external__search_posts"]
	assert.False(t, hasExternal, "external tool with a colliding bare name must not get a guardrail hook")
}

func TestAIPromptAction_Execute_NoToolHooksWithoutGuardrailChannels(t *testing.T) {
	api := newTestAPI()
	bc := &mockBridgeClient{
		agentResponse: "ok",
		agentTools:    []bridgeclient.BridgeToolInfo{mmTool("search_posts")},
	}
	a := NewAIPromptAction(api, bc, "")

	act := &model.Action{
		ID: "ai-step",
		AIPrompt: &model.AIPromptActionConfig{
			Prompt:       "q",
			ProviderType: "agent",
			ProviderID:   "ai-bot",
			AllowedTools: []string{"search_posts"},
			Guardrails:   &model.Guardrails{Channels: []model.GuardrailChannel{}},
		},
	}
	ctx := &model.AutomationContext{AutomationID: "f1", CreatedBy: "creator1", Trigger: model.TriggerData{}, Steps: make(map[string]model.StepOutput)}

	_, err := a.Execute(act, ctx)
	require.NoError(t, err)
	assert.Nil(t, bc.lastReq.ToolHooks)
}

func TestAIPromptAction_Execute_AllowedTools(t *testing.T) {
	api := newTestAPI()
	bc := &mockBridgeClient{
		agentResponse: "tool result",
		agentTools:    []bridgeclient.BridgeToolInfo{{Name: "search"}},
	}
	a := NewAIPromptAction(api, bc, "")

	act := &model.Action{
		ID: "ai1",
		AIPrompt: &model.AIPromptActionConfig{
			Prompt:       "Do something",
			ProviderType: "agent",
			ProviderID:   "ai-bot",
			AllowedTools: []string{"search"},
		},
	}
	ctx := &model.AutomationContext{
		CreatedBy: "creator1",
		Trigger:   model.TriggerData{},
		Steps:     make(map[string]model.StepOutput),
	}

	output, err := a.Execute(act, ctx)
	require.NoError(t, err)
	require.NotNil(t, output)
	assert.Equal(t, "tool result", output.Message)
	assert.Equal(t, []string{"search"}, bc.lastReq.AllowedTools)
}

func TestAIPromptAction_Execute_AllowedTools_RejectedAtRuntime(t *testing.T) {
	api := newTestAPI()
	bc := &mockBridgeClient{agentResponse: "ok", agentTools: []bridgeclient.BridgeToolInfo{}}
	a := NewAIPromptAction(api, bc, "")

	act := &model.Action{
		ID: "ai1",
		AIPrompt: &model.AIPromptActionConfig{
			Prompt:       "Do something",
			ProviderType: "agent",
			ProviderID:   "ai-bot",
			AllowedTools: []string{"search_posts"},
		},
	}
	ctx := &model.AutomationContext{CreatedBy: "creator1", Trigger: model.TriggerData{}, Steps: make(map[string]model.StepOutput)}

	output, err := a.Execute(act, ctx)
	require.Error(t, err)
	assert.Nil(t, output)
	assert.Contains(t, err.Error(), "allowed_tools validation failed")
}

func TestAIPromptAction_Execute_AllowedTools_DemotedToolRejected(t *testing.T) {
	api := newTestAPI()
	bc := &mockBridgeClient{agentResponse: "ok", agentTools: []bridgeclient.BridgeToolInfo{mmTool("create_post")}}
	a := NewAIPromptAction(api, bc, "")

	act := &model.Action{
		ID: "ai1",
		AIPrompt: &model.AIPromptActionConfig{
			Prompt:       "post something",
			ProviderType: "agent",
			ProviderID:   "ai-bot",
			AllowedTools: []string{"create_post"},
		},
	}
	ctx := &model.AutomationContext{CreatedBy: "creator1", Trigger: model.TriggerData{}, Steps: make(map[string]model.StepOutput)}

	_, err := a.Execute(act, ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not permitted in automations")
}

func TestAIPromptAction_Execute_NoToolFields(t *testing.T) {
	api := newTestAPI()
	bc := &mockBridgeClient{agentResponse: "ok"}
	a := NewAIPromptAction(api, bc, "")

	act := &model.Action{
		ID: "ai1",
		AIPrompt: &model.AIPromptActionConfig{
			Prompt:       "hello",
			ProviderType: "agent",
			ProviderID:   "ai-bot",
		},
	}
	ctx := &model.AutomationContext{
		Trigger: model.TriggerData{},
		Steps:   make(map[string]model.StepOutput),
	}

	output, err := a.Execute(act, ctx)
	require.NoError(t, err)
	require.NotNil(t, output)
	assert.Nil(t, bc.lastReq.AllowedTools)
}

func TestAIPromptAction_Execute_SystemPromptRendered(t *testing.T) {
	api := newTestAPI()
	bc := &mockBridgeClient{agentResponse: "response"}
	a := NewAIPromptAction(api, bc, "")

	act := &model.Action{
		ID: "ai1",
		AIPrompt: &model.AIPromptActionConfig{
			SystemPrompt: "You are a helpful assistant for {{.Trigger.User.Username}}.",
			Prompt:       "Summarize this.",
			ProviderType: "agent",
			ProviderID:   "ai-bot",
		},
	}
	ctx := &model.AutomationContext{
		Trigger: model.TriggerData{
			User: &model.SafeUser{Id: "user1", Username: "alice"},
		},
		Steps: make(map[string]model.StepOutput),
	}

	output, err := a.Execute(act, ctx)
	require.NoError(t, err)
	require.NotNil(t, output)
	assert.Equal(t, "response", output.Message)
	// Posts: [custom system prompt, trigger metadata (system), user-generated content (user), user prompt (user)]
	require.Len(t, bc.lastReq.Posts, 4)
	assert.Equal(t, "system", bc.lastReq.Posts[0].Role)
	assert.Equal(t, "You are a helpful assistant for alice.", bc.lastReq.Posts[0].Message)
	assert.Equal(t, "system", bc.lastReq.Posts[1].Role)
	assert.Contains(t, bc.lastReq.Posts[1].Message, "<trigger_context>")
	assert.Contains(t, bc.lastReq.Posts[1].Message, "Triggering User ID: user1")
	assert.NotContains(t, bc.lastReq.Posts[1].Message, "alice")
	assert.Equal(t, "user", bc.lastReq.Posts[2].Role)
	assert.Contains(t, bc.lastReq.Posts[2].Message, "Triggering Username: alice")
	assert.Equal(t, "user", bc.lastReq.Posts[3].Role)
	assert.Equal(t, "Summarize this.", bc.lastReq.Posts[3].Message)
}

func TestAIPromptAction_Execute_EmptySystemPromptNoUserSystemPost(t *testing.T) {
	api := newTestAPI()
	bc := &mockBridgeClient{agentResponse: "response"}
	a := NewAIPromptAction(api, bc, "")

	act := &model.Action{
		ID: "ai1",
		AIPrompt: &model.AIPromptActionConfig{
			Prompt:       "Hello",
			ProviderType: "agent",
			ProviderID:   "ai-bot",
		},
	}
	ctx := &model.AutomationContext{
		Trigger: model.TriggerData{},
		Steps:   make(map[string]model.StepOutput),
	}

	output, err := a.Execute(act, ctx)
	require.NoError(t, err)
	require.NotNil(t, output)
	// Even with no user system prompt and empty trigger, scope instruction plus
	// the auto-injected current date are always present.
	require.Len(t, bc.lastReq.Posts, 2)
	assert.Equal(t, "system", bc.lastReq.Posts[0].Role)
	assert.Contains(t, bc.lastReq.Posts[0].Message, "Complete only the specific task")
	assert.Contains(t, bc.lastReq.Posts[0].Message, "<trigger_context>")
	assert.Contains(t, bc.lastReq.Posts[0].Message, "Current Date: ")
	assert.Equal(t, "user", bc.lastReq.Posts[1].Role)
	assert.Equal(t, "Hello", bc.lastReq.Posts[1].Message)
}

func TestAIPromptAction_Execute_BadSystemPromptTemplate(t *testing.T) {
	api := newTestAPI()
	bc := &mockBridgeClient{}
	a := NewAIPromptAction(api, bc, "")

	act := &model.Action{
		ID: "ai1",
		AIPrompt: &model.AIPromptActionConfig{
			SystemPrompt: "{{.Invalid",
			Prompt:       "hello",
			ProviderType: "agent",
			ProviderID:   "bot",
		},
	}
	ctx := &model.AutomationContext{Trigger: model.TriggerData{}, Steps: make(map[string]model.StepOutput)}

	output, err := a.Execute(act, ctx)
	require.Error(t, err)
	assert.Nil(t, output)
	assert.Contains(t, err.Error(), "failed to render system prompt template")
}

func TestBuildTriggerContext(t *testing.T) {
	t.Run("empty trigger still includes current date and unix timestamp", func(t *testing.T) {
		meta, userContent := buildTriggerContext(model.TriggerData{}, fixedTime)
		assert.Contains(t, meta, "<trigger_context>")
		assert.Contains(t, meta, "Current Date: 2026-04-22T14:30:45Z (Wednesday)")
		assert.Contains(t, meta, fmt.Sprintf("Current Unix Timestamp (ms): %d", model.TimeToTimestamp(fixedTime)))
		assert.Empty(t, userContent)
	})

	t.Run("current date is emitted in UTC regardless of input location", func(t *testing.T) {
		loc := time.FixedZone("America/New_York", -4*60*60)
		nyTime := time.Date(2026, time.April, 22, 10, 30, 45, 0, loc) // 14:30:45 UTC
		meta, _ := buildTriggerContext(model.TriggerData{}, nyTime)
		assert.Contains(t, meta, "Current Date: 2026-04-22T14:30:45Z (Wednesday)")
		assert.Contains(t, meta, fmt.Sprintf("Current Unix Timestamp (ms): %d", model.TimeToTimestamp(nyTime)))
	})

	t.Run("post trigger separates metadata from user content", func(t *testing.T) {
		meta, userContent := buildTriggerContext(model.TriggerData{
			Post: &model.SafePost{
				Id:        "post123",
				ThreadId:  "thread456",
				ChannelId: "chan789",
				Message:   "Alert: server is down",
			},
			Channel: &model.SafeChannel{
				Id:          "chan789",
				Name:        "incidents",
				DisplayName: "Incidents",
			},
			User: &model.SafeUser{
				Id:       "user1",
				Username: "sysadmin",
			},
		}, fixedTime)
		// Metadata (system-safe) should contain only IDs
		assert.Contains(t, meta, "<trigger_context>")
		assert.Contains(t, meta, "Post ID: post123")
		assert.Contains(t, meta, "Thread ID: thread456")
		assert.Contains(t, meta, "Channel ID: chan789")
		assert.Contains(t, meta, "Triggering User ID: user1")
		assert.Equal(t, 1, strings.Count(meta, "Channel ID:"))
		assert.Contains(t, meta, "Channel Name: incidents")
		// User-generated fields must NOT be in metadata
		assert.NotContains(t, meta, "Alert: server is down")
		assert.NotContains(t, meta, "Incidents")
		assert.NotContains(t, meta, "sysadmin")

		// User content should contain user-generated fields with untrusted warning
		assert.Contains(t, userContent, "Alert: server is down")
		assert.Contains(t, userContent, "Channel Display Name: Incidents")
		assert.NotContains(t, userContent, "Channel Name: incidents")
		assert.Contains(t, userContent, "Triggering Username: sysadmin")
		assert.Contains(t, userContent, "<user_data>")
	})

	t.Run("schedule trigger", func(t *testing.T) {
		meta, userContent := buildTriggerContext(model.TriggerData{
			Schedule: &model.ScheduleInfo{
				Interval: "daily",
				FiredAt:  1700000000000,
			},
		}, fixedTime)
		assert.Contains(t, meta, "<trigger_context>")
		assert.Contains(t, meta, "Schedule Interval: daily")
		assert.Contains(t, meta, "Fired At: 1700000000000")
		assert.NotContains(t, meta, "Post ID")
		assert.NotContains(t, meta, "Triggering User")
		assert.Empty(t, userContent)
	})

	t.Run("channel only trigger", func(t *testing.T) {
		meta, userContent := buildTriggerContext(model.TriggerData{
			Channel: &model.SafeChannel{
				Id:   "chan1",
				Name: "general",
			},
		}, fixedTime)
		assert.Contains(t, meta, "Channel ID: chan1")
		assert.Contains(t, meta, "Channel Name: general")
		assert.Empty(t, userContent)
	})

	t.Run("post with empty message produces no user content", func(t *testing.T) {
		meta, userContent := buildTriggerContext(model.TriggerData{
			Post: &model.SafePost{
				Id: "post123",
			},
		}, fixedTime)
		assert.Contains(t, meta, "Post ID: post123")
		assert.Empty(t, userContent)
	})
}

func TestAIPromptAction_Execute_TriggerContextInjected(t *testing.T) {
	api := newTestAPI()
	bc := &mockBridgeClient{agentResponse: "done"}
	a := NewAIPromptAction(api, bc, "")

	act := &model.Action{
		ID: "ai1",
		AIPrompt: &model.AIPromptActionConfig{
			Prompt:       "Handle this incident",
			ProviderType: "agent",
			ProviderID:   "ai-bot",
		},
	}
	ctx := &model.AutomationContext{
		Trigger: model.TriggerData{
			Post: &model.SafePost{
				Id:        "post123",
				ThreadId:  "thread456",
				ChannelId: "chan789",
				Message:   "Postgres is down in production",
			},
			Channel: &model.SafeChannel{
				Id:          "chan789",
				Name:        "incidents",
				DisplayName: "Incidents",
			},
			User: &model.SafeUser{
				Id:       "user1",
				Username: "sysadmin",
			},
		},
		Steps: make(map[string]model.StepOutput),
	}

	output, err := a.Execute(act, ctx)
	require.NoError(t, err)
	require.NotNil(t, output)

	// Should have: trigger metadata (system) + user-generated post (user) + user prompt (user)
	require.Len(t, bc.lastReq.Posts, 3)

	// System message: only trusted IDs
	triggerMeta := bc.lastReq.Posts[0]
	assert.Equal(t, "system", triggerMeta.Role)
	assert.Contains(t, triggerMeta.Message, "Post ID: post123")
	assert.Contains(t, triggerMeta.Message, "Thread ID: thread456")
	assert.Contains(t, triggerMeta.Message, "Channel ID: chan789")
	assert.Contains(t, triggerMeta.Message, "Triggering User ID: user1")
	assert.Contains(t, triggerMeta.Message, "Complete only the specific task")
	assert.Contains(t, triggerMeta.Message, "Channel Name: incidents")
	// User-generated fields must NOT be in system prompt
	assert.NotContains(t, triggerMeta.Message, "Postgres is down in production")
	assert.NotContains(t, triggerMeta.Message, "Incidents")
	assert.NotContains(t, triggerMeta.Message, "sysadmin")

	// User message: all user-generated content with untrusted warning
	userGenerated := bc.lastReq.Posts[1]
	assert.Equal(t, "user", userGenerated.Role)
	assert.Contains(t, userGenerated.Message, "Postgres is down in production")
	assert.Contains(t, userGenerated.Message, "Channel Display Name: Incidents")
	assert.Contains(t, userGenerated.Message, "Triggering Username: sysadmin")
	assert.Contains(t, userGenerated.Message, "<user_data>")

	assert.Equal(t, "user", bc.lastReq.Posts[2].Role)
	assert.Equal(t, "Handle this incident", bc.lastReq.Posts[2].Message)
}

func TestAIPromptAction_Execute_ThreadContext_InjectsTranscriptAndMetadata(t *testing.T) {
	api := newTestAPI()
	bc := &mockBridgeClient{agentResponse: "ok"}
	a := NewAIPromptAction(api, bc, "")

	act := &model.Action{
		ID: "ai1",
		AIPrompt: &model.AIPromptActionConfig{
			Prompt:       "Reply to the thread",
			ProviderType: "agent",
			ProviderID:   "ai-bot",
		},
	}
	// Thread is populated by the message_posted trigger handler when the
	// firing post is itself a reply; the action just consumes it.
	ctx := &model.AutomationContext{
		Trigger: model.TriggerData{
			Post: &model.SafePost{Id: "replyp", ThreadId: "rootp", ChannelId: "ch1", Message: "world"},
			Thread: &model.SafeThread{
				RootID:    "rootp",
				PostCount: 2,
				Messages: []model.SafePost{
					{Id: "rootp", User: model.SafeUser{Username: "alice", FirstName: "Alice", LastName: "A."}, Message: "hello", CreateAt: 100},
					{Id: "replyp", User: model.SafeUser{Username: "bob", FirstName: "Bob", LastName: "B."}, Message: "world", CreateAt: 200},
				},
			},
		},
		Steps: make(map[string]model.StepOutput),
	}

	_, err := a.Execute(act, ctx)
	require.NoError(t, err)

	// Posts: [trigger metadata (system), <user_data> with transcript (user), final prompt (user)]
	require.Len(t, bc.lastReq.Posts, 3)

	triggerMeta := bc.lastReq.Posts[0]
	assert.Equal(t, "system", triggerMeta.Role)
	assert.Contains(t, triggerMeta.Message, "Thread Post Count: 2")
	assert.Contains(t, triggerMeta.Message, "Thread Root ID: rootp")
	// User-generated content must NOT leak into the system block.
	assert.NotContains(t, triggerMeta.Message, "hello")
	assert.NotContains(t, triggerMeta.Message, "world")

	userContent := bc.lastReq.Posts[1]
	assert.Equal(t, "user", userContent.Role)
	assert.Contains(t, userContent.Message, "<user_data>")
	assert.Contains(t, userContent.Message, "Thread Transcript (oldest first):")
	assert.Contains(t, userContent.Message, "@alice (Alice A.): hello")
	assert.Contains(t, userContent.Message, "@bob (Bob B.): world")

	assert.Equal(t, "user", bc.lastReq.Posts[2].Role)
	assert.Equal(t, "Reply to the thread", bc.lastReq.Posts[2].Message)
}

func TestAIPromptAction_Execute_ThreadContext_DisclosesTruncationToModel(t *testing.T) {
	api := newTestAPI()
	bc := &mockBridgeClient{agentResponse: "ok"}
	a := NewAIPromptAction(api, bc, "")

	act := &model.Action{
		ID: "ai1",
		AIPrompt: &model.AIPromptActionConfig{
			Prompt:       "Reply to the thread",
			ProviderType: "agent",
			ProviderID:   "ai-bot",
		},
	}
	// Synthetic truncated thread: original count higher than len(Messages),
	// Truncated=true. The trigger metadata system block must surface this
	// so the model knows it is not seeing the full conversation.
	ctx := &model.AutomationContext{
		Trigger: model.TriggerData{
			Post: &model.SafePost{Id: "replyp", ThreadId: "rootp", ChannelId: "ch1", Message: "x"},
			Thread: &model.SafeThread{
				RootID:    "rootp",
				PostCount: 250,
				Truncated: true,
				Messages: []model.SafePost{
					{Id: "rootp", User: model.SafeUser{Username: "alice"}, Message: "topic", CreateAt: 0},
					{Id: "replyp", User: model.SafeUser{Username: "bob"}, Message: "latest", CreateAt: 250},
				},
			},
		},
		Steps: make(map[string]model.StepOutput),
	}

	_, err := a.Execute(act, ctx)
	require.NoError(t, err)

	triggerMeta := bc.lastReq.Posts[0]
	assert.Contains(t, triggerMeta.Message, "Thread Post Count: 250")
	assert.Contains(t, triggerMeta.Message, "Thread Truncated: true")
	assert.Contains(t, triggerMeta.Message, "most recent 1 replies")
}

func TestAIPromptAction_Execute_ThreadContext_NotInjectedWhenAbsent(t *testing.T) {
	api := newTestAPI()
	bc := &mockBridgeClient{agentResponse: "ok"}
	a := NewAIPromptAction(api, bc, "")

	act := &model.Action{
		ID: "ai1",
		AIPrompt: &model.AIPromptActionConfig{
			Prompt:       "Greet the post author",
			ProviderType: "agent",
			ProviderID:   "ai-bot",
		},
	}
	// Root-post fire: trigger handler did not attach a Thread.
	ctx := &model.AutomationContext{
		Trigger: model.TriggerData{
			Post: &model.SafePost{Id: "rootp", ThreadId: "rootp", ChannelId: "ch1", Message: "hi"},
		},
		Steps: make(map[string]model.StepOutput),
	}

	_, err := a.Execute(act, ctx)
	require.NoError(t, err)
	// Posts: [trigger metadata (system), <user_data> (user), final prompt (user)]
	require.Len(t, bc.lastReq.Posts, 3)
	assert.NotContains(t, bc.lastReq.Posts[0].Message, "Thread Post Count")
	assert.NotContains(t, bc.lastReq.Posts[1].Message, "Thread Transcript")
}

func TestAIPromptAction_Execute_UserIDFromTrigger(t *testing.T) {
	tests := []struct {
		name      string
		createdBy string
	}{
		{name: "both set, trigger wins", createdBy: "automation-creator-id"},
		{name: "empty CreatedBy, trigger still wins", createdBy: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			api := newTestAPI()
			bc := &mockBridgeClient{agentResponse: "ok"}
			a := NewAIPromptAction(api, bc, "")

			act := &model.Action{
				ID: "ai1",
				AIPrompt: &model.AIPromptActionConfig{
					Prompt:       "hello",
					ProviderType: "agent",
					ProviderID:   "ai-bot",
				},
			}
			ctx := &model.AutomationContext{
				CreatedBy: tc.createdBy,
				Trigger: model.TriggerData{
					User: &model.SafeUser{Id: "triggering-user-id", Username: "bob"},
				},
				Steps: make(map[string]model.StepOutput),
			}

			_, err := a.Execute(act, ctx)
			require.NoError(t, err)
			assert.Equal(t, "triggering-user-id", bc.lastReq.UserID)
		})
	}
}

func TestAIPromptAction_Execute_UserIDFallbackToCreatedBy(t *testing.T) {
	tests := []struct {
		name    string
		trigger model.TriggerData
	}{
		{
			name:    "no trigger user (e.g. schedule)",
			trigger: model.TriggerData{Schedule: &model.ScheduleInfo{Interval: "1h"}},
		},
		{
			name:    "trigger user with empty id",
			trigger: model.TriggerData{User: &model.SafeUser{Username: "anon"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			api := newTestAPI()
			bc := &mockBridgeClient{agentResponse: "ok"}
			a := NewAIPromptAction(api, bc, "")

			act := &model.Action{
				ID: "ai1",
				AIPrompt: &model.AIPromptActionConfig{
					Prompt:       "hello",
					ProviderType: "agent",
					ProviderID:   "ai-bot",
				},
			}
			ctx := &model.AutomationContext{
				CreatedBy: "automation-creator-id",
				Trigger:   tc.trigger,
				Steps:     make(map[string]model.StepOutput),
			}

			_, err := a.Execute(act, ctx)
			require.NoError(t, err)
			assert.Equal(t, "automation-creator-id", bc.lastReq.UserID)
		})
	}
}

func TestAIPromptAction_Execute_RequestAs(t *testing.T) {
	tests := []struct {
		name      string
		requestAs string
		ctx       *model.AutomationContext
		want      string
	}{
		{
			name:      "creator forces automation creator over trigger user",
			requestAs: "creator",
			ctx: &model.AutomationContext{
				CreatedBy: "automation-creator-id",
				Trigger:   model.TriggerData{User: &model.SafeUser{Id: "triggering-user-id"}},
				Steps:     make(map[string]model.StepOutput),
			},
			want: "automation-creator-id",
		},
		{
			name:      "creator with no trigger user still uses creator",
			requestAs: "creator",
			ctx: &model.AutomationContext{
				CreatedBy: "automation-creator-id",
				Trigger:   model.TriggerData{Schedule: &model.ScheduleInfo{Interval: "1h"}},
				Steps:     make(map[string]model.StepOutput),
			},
			want: "automation-creator-id",
		},
		{
			name:      "triggerer prefers trigger user",
			requestAs: "triggerer",
			ctx: &model.AutomationContext{
				CreatedBy: "automation-creator-id",
				Trigger:   model.TriggerData{User: &model.SafeUser{Id: "triggering-user-id"}},
				Steps:     make(map[string]model.StepOutput),
			},
			want: "triggering-user-id",
		},
		{
			name:      "triggerer falls back to creator when no trigger user",
			requestAs: "triggerer",
			ctx: &model.AutomationContext{
				CreatedBy: "automation-creator-id",
				Trigger:   model.TriggerData{Schedule: &model.ScheduleInfo{Interval: "1h"}},
				Steps:     make(map[string]model.StepOutput),
			},
			want: "automation-creator-id",
		},
		{
			name:      "empty defaults to triggerer behavior",
			requestAs: "",
			ctx: &model.AutomationContext{
				CreatedBy: "automation-creator-id",
				Trigger:   model.TriggerData{User: &model.SafeUser{Id: "triggering-user-id"}},
				Steps:     make(map[string]model.StepOutput),
			},
			want: "triggering-user-id",
		},
		{
			name:      "membership_changed with explicit creator uses creator",
			requestAs: "creator",
			ctx: &model.AutomationContext{
				CreatedBy:   "automation-creator-id",
				TriggerType: model.TriggerTypeMembershipChanged,
				Trigger: model.TriggerData{
					User:       &model.SafeUser{Id: "triggering-user-id"},
					Membership: &model.MembershipInfo{Action: "joined"},
				},
				Steps: make(map[string]model.StepOutput),
			},
			want: "automation-creator-id",
		},
		{
			name:      "user_joined_team with explicit creator uses creator",
			requestAs: "creator",
			ctx: &model.AutomationContext{
				CreatedBy:   "automation-creator-id",
				TriggerType: model.TriggerTypeUserJoinedTeam,
				Trigger: model.TriggerData{
					User: &model.SafeUser{Id: "joining-user-id"},
				},
				Steps: make(map[string]model.StepOutput),
			},
			want: "automation-creator-id",
		},
		{
			name:      "channel_created with explicit creator uses creator",
			requestAs: "creator",
			ctx: &model.AutomationContext{
				CreatedBy:   "automation-creator-id",
				TriggerType: model.TriggerTypeChannelCreated,
				Trigger: model.TriggerData{
					User: &model.SafeUser{Id: "channel-creator-id"},
				},
				Steps: make(map[string]model.StepOutput),
			},
			want: "automation-creator-id",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			api := newTestAPI()
			bc := &mockBridgeClient{agentResponse: "ok"}
			a := NewAIPromptAction(api, bc, "")

			act := &model.Action{
				ID: "ai1",
				AIPrompt: &model.AIPromptActionConfig{
					Prompt:       "hello",
					ProviderType: "agent",
					ProviderID:   "ai-bot",
					RequestAs:    tc.requestAs,
				},
			}

			_, err := a.Execute(act, tc.ctx)
			require.NoError(t, err)
			assert.Equal(t, tc.want, bc.lastReq.UserID)
		})
	}
}

func TestAIPromptAction_Execute_CreatorLockedRejectsTriggerer(t *testing.T) {
	cases := []struct {
		triggerType string
		membership  *model.MembershipInfo
	}{
		{model.TriggerTypeMembershipChanged, &model.MembershipInfo{Action: "joined"}},
		{model.TriggerTypeUserJoinedTeam, nil},
		{model.TriggerTypeChannelCreated, nil},
	}
	for _, tc := range cases {
		for _, requestAs := range []string{"triggerer", ""} {
			t.Run(tc.triggerType+"/request_as="+requestAs, func(t *testing.T) {
				api := newTestAPI()
				bc := &mockBridgeClient{agentResponse: "ok"}
				a := NewAIPromptAction(api, bc, "")

				act := &model.Action{
					ID: "ai1",
					AIPrompt: &model.AIPromptActionConfig{
						Prompt:       "hello",
						ProviderType: "agent",
						ProviderID:   "ai-bot",
						RequestAs:    requestAs,
					},
				}
				ctx := &model.AutomationContext{
					CreatedBy:   "automation-creator-id",
					TriggerType: tc.triggerType,
					Trigger: model.TriggerData{
						User:       &model.SafeUser{Id: "triggering-user-id"},
						Membership: tc.membership,
					},
					Steps: make(map[string]model.StepOutput),
				}

				output, err := a.Execute(act, ctx)
				require.Error(t, err)
				assert.Nil(t, output)
				assert.Contains(t, err.Error(), "must set request_as")
				assert.Empty(t, bc.lastReq.UserID, "bridge must not be called when validation fails")
			})
		}
	}
}

func TestAIPromptAction_Execute_RejectsBotUser(t *testing.T) {
	t.Run("bot triggerer", func(t *testing.T) {
		api := &plugintest.API{}
		api.On("LogDebug", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
		api.On("GetUser", "bot-triggerer").Return(&mmmodel.User{Id: "bot-triggerer", IsBot: true}, nil)

		bc := &mockBridgeClient{agentResponse: "ok"}
		a := NewAIPromptAction(api, bc, "")
		act := &model.Action{
			ID: "ai1",
			AIPrompt: &model.AIPromptActionConfig{
				Prompt: "hello", ProviderType: "agent", ProviderID: "ai-bot",
				RequestAs: "triggerer",
			},
		}
		ctx := &model.AutomationContext{
			CreatedBy:   "automation-creator-id",
			TriggerType: model.TriggerTypeMessagePosted,
			Trigger:     model.TriggerData{User: &model.SafeUser{Id: "bot-triggerer"}},
			Steps:       make(map[string]model.StepOutput),
		}
		output, err := a.Execute(act, ctx)
		require.Error(t, err)
		assert.Nil(t, output)
		assert.Contains(t, err.Error(), "cannot run as bot")
		assert.Empty(t, bc.lastReq.UserID)
	})

	t.Run("bot creator", func(t *testing.T) {
		api := &plugintest.API{}
		api.On("LogDebug", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
		api.On("GetUser", "bot-creator").Return(&mmmodel.User{Id: "bot-creator", IsBot: true}, nil)

		bc := &mockBridgeClient{agentResponse: "ok"}
		a := NewAIPromptAction(api, bc, "")
		act := &model.Action{
			ID: "ai1",
			AIPrompt: &model.AIPromptActionConfig{
				Prompt: "hello", ProviderType: "agent", ProviderID: "ai-bot",
				RequestAs: "creator",
			},
		}
		ctx := &model.AutomationContext{
			CreatedBy:   "bot-creator",
			TriggerType: model.TriggerTypeMessagePosted,
			Trigger:     model.TriggerData{User: &model.SafeUser{Id: "human"}},
			Steps:       make(map[string]model.StepOutput),
		}
		output, err := a.Execute(act, ctx)
		require.Error(t, err)
		assert.Nil(t, output)
		assert.Contains(t, err.Error(), "cannot run as bot")
		assert.Empty(t, bc.lastReq.UserID)
	})

	t.Run("lookup error fails closed", func(t *testing.T) {
		api := &plugintest.API{}
		api.On("LogDebug", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
		api.On("GetUser", "creator1").Return(nil, mmmodel.NewAppError("GetUser", "app.user.get.app_error", nil, "", 500))

		bc := &mockBridgeClient{agentResponse: "ok"}
		a := NewAIPromptAction(api, bc, "")
		act := &model.Action{
			ID: "ai1",
			AIPrompt: &model.AIPromptActionConfig{
				Prompt: "hello", ProviderType: "agent", ProviderID: "ai-bot",
				RequestAs: "creator",
			},
		}
		ctx := &model.AutomationContext{
			CreatedBy:   "creator1",
			TriggerType: model.TriggerTypeSchedule,
			Steps:       make(map[string]model.StepOutput),
		}
		output, err := a.Execute(act, ctx)
		require.Error(t, err)
		assert.Nil(t, output)
		assert.Contains(t, err.Error(), "failed to verify ai_prompt user")
		assert.Empty(t, bc.lastReq.UserID)
	})
}

func TestAIPromptAction_Execute_CreatorOnlyToolsRequireCreator(t *testing.T) {
	api := newTestAPI()
	// The tool must be available so allowed_tools re-validation passes and the
	// creator-only gate (which now runs after it) is what rejects the run.
	bc := &mockBridgeClient{agentResponse: "ok", agentTools: []bridgeclient.BridgeToolInfo{mmTool("search_users")}}
	a := NewAIPromptAction(api, bc, "")

	act := &model.Action{
		ID: "ai1",
		AIPrompt: &model.AIPromptActionConfig{
			Prompt:       "hello",
			ProviderType: "agent",
			ProviderID:   "ai-bot",
			RequestAs:    "triggerer",
			AllowedTools: []string{"search_users"},
		},
	}
	ctx := &model.AutomationContext{
		CreatedBy:   "automation-creator-id",
		TriggerType: model.TriggerTypeMessagePosted,
		Trigger:     model.TriggerData{User: &model.SafeUser{Id: "poster-id"}},
		Steps:       make(map[string]model.StepOutput),
	}
	output, err := a.Execute(act, ctx)
	require.Error(t, err)
	assert.Nil(t, output)
	assert.Contains(t, err.Error(), "requires request_as")
	assert.Empty(t, bc.lastReq.UserID)
}

// TestAIPromptAction_Execute_UnguardedExternalToolAllowsTriggerer verifies that
// external / unconstrained tools are unaffected by the creator-only rule and
// may run as the triggerer without guardrails.
func TestAIPromptAction_Execute_UnguardedExternalToolAllowsTriggerer(t *testing.T) {
	api := newTestAPI()
	bc := &mockBridgeClient{
		agentResponse: "ok",
		agentTools: []bridgeclient.BridgeToolInfo{{
			Name: "external__search", ServerOrigin: "external-mcp",
		}},
	}
	a := NewAIPromptAction(api, bc, "")

	act := &model.Action{
		ID: "ai1",
		AIPrompt: &model.AIPromptActionConfig{
			Prompt:       "search external",
			ProviderType: "agent",
			ProviderID:   "ai-bot",
			RequestAs:    model.AIPromptRequestAsTriggerer,
			AllowedTools: []string{"external__search"},
		},
	}
	ctx := &model.AutomationContext{
		CreatedBy:   "automation-creator-id",
		TriggerType: model.TriggerTypeMessagePosted,
		Trigger:     model.TriggerData{User: &model.SafeUser{Id: "poster-id"}},
		Steps:       make(map[string]model.StepOutput),
	}

	output, err := a.Execute(act, ctx)
	require.NoError(t, err)
	require.NotNil(t, output)
	assert.Equal(t, "poster-id", bc.lastReq.UserID)
}

// TestAIPromptAction_Execute_CreatorOnlyToolsAllowedForSchedule verifies a
// schedule trigger (which has no triggering user and always resolves to the
// creator) may use creator-only tools even when request_as is not "creator".
func TestAIPromptAction_Execute_CreatorOnlyToolsAllowedForSchedule(t *testing.T) {
	api := newTestAPI()
	bc := &mockBridgeClient{
		agentResponse: "ok",
		agentTools:    []bridgeclient.BridgeToolInfo{mmTool("search_users")},
	}
	a := NewAIPromptAction(api, bc, "")

	act := &model.Action{
		ID: "ai1",
		AIPrompt: &model.AIPromptActionConfig{
			Prompt:       "hello",
			ProviderType: "agent",
			ProviderID:   "ai-bot",
			RequestAs:    "triggerer",
			AllowedTools: []string{"search_users"},
		},
	}
	ctx := &model.AutomationContext{
		CreatedBy:   "automation-creator-id",
		TriggerType: model.TriggerTypeSchedule,
		Trigger:     model.TriggerData{},
		Steps:       make(map[string]model.StepOutput),
	}
	output, err := a.Execute(act, ctx)
	require.NoError(t, err)
	require.NotNil(t, output)
	assert.Equal(t, "automation-creator-id", bc.lastReq.UserID)
}

func TestAIPromptAction_Execute_UnsupportedProviderType(t *testing.T) {
	api := newTestAPI()
	bc := &mockBridgeClient{}
	a := NewAIPromptAction(api, bc, "")

	act := &model.Action{
		ID: "ai1",
		AIPrompt: &model.AIPromptActionConfig{
			Prompt:       "hello",
			ProviderType: "unknown",
			ProviderID:   "bot",
		},
	}
	ctx := &model.AutomationContext{Trigger: model.TriggerData{}, Steps: make(map[string]model.StepOutput)}

	output, err := a.Execute(act, ctx)
	require.Error(t, err)
	assert.Nil(t, output)
	assert.Contains(t, err.Error(), `unsupported provider_type "unknown"`)
}

// ---- Typing indicator tests ----

const testBotUserID = "bot-user-id"

func validAIPromptAction() *model.Action {
	return &model.Action{
		ID: "ai1",
		AIPrompt: &model.AIPromptActionConfig{
			Prompt:       "hello",
			ProviderType: "agent",
			ProviderID:   "ai-bot",
		},
	}
}

func TestAIPromptAction_Execute_PublishesTypingForChannel(t *testing.T) {
	api := newTestAPI()
	api.On("PublishUserTyping", testBotUserID, "C1", "").Return(nil)

	bc := &mockBridgeClient{agentResponse: "ok"}
	a := NewAIPromptAction(api, bc, testBotUserID)

	ctx := &model.AutomationContext{
		Trigger: model.TriggerData{
			Channel: &model.SafeChannel{Id: "C1", Name: "general"},
		},
		Steps: make(map[string]model.StepOutput),
	}

	_, err := a.Execute(validAIPromptAction(), ctx)
	require.NoError(t, err)
	api.AssertCalled(t, "PublishUserTyping", testBotUserID, "C1", "")
}

func TestAIPromptAction_Execute_PublishesTypingForThread(t *testing.T) {
	api := newTestAPI()
	api.On("PublishUserTyping", testBotUserID, "C1", "rootp").Return(nil)

	bc := &mockBridgeClient{agentResponse: "ok"}
	a := NewAIPromptAction(api, bc, testBotUserID)

	ctx := &model.AutomationContext{
		Trigger: model.TriggerData{
			Channel: &model.SafeChannel{Id: "C1"},
			Thread:  &model.SafeThread{RootID: "rootp", PostCount: 1},
		},
		Steps: make(map[string]model.StepOutput),
	}

	_, err := a.Execute(validAIPromptAction(), ctx)
	require.NoError(t, err)
	api.AssertCalled(t, "PublishUserTyping", testBotUserID, "C1", "rootp")
}

func TestAIPromptAction_Execute_SkipsTypingWhenNoChannel(t *testing.T) {
	api := newTestAPI()
	// Deliberately do NOT stub PublishUserTyping: AssertNotCalled below would
	// still pass if a Return wasn't set, but leaving it unstubbed catches
	// accidental publishes via panic on an unexpected call.

	bc := &mockBridgeClient{agentResponse: "ok"}
	a := NewAIPromptAction(api, bc, testBotUserID)

	ctx := &model.AutomationContext{
		Trigger: model.TriggerData{
			Schedule: &model.ScheduleInfo{Interval: "1h"},
		},
		Steps: make(map[string]model.StepOutput),
	}

	_, err := a.Execute(validAIPromptAction(), ctx)
	require.NoError(t, err)
	api.AssertNotCalled(t, "PublishUserTyping", mock.Anything, mock.Anything, mock.Anything)
}

func TestAIPromptAction_Execute_SkipsTypingWhenNoBotUserID(t *testing.T) {
	api := newTestAPI()

	bc := &mockBridgeClient{agentResponse: "ok"}
	a := NewAIPromptAction(api, bc, "")

	ctx := &model.AutomationContext{
		Trigger: model.TriggerData{
			Channel: &model.SafeChannel{Id: "C1"},
		},
		Steps: make(map[string]model.StepOutput),
	}

	_, err := a.Execute(validAIPromptAction(), ctx)
	require.NoError(t, err)
	api.AssertNotCalled(t, "PublishUserTyping", mock.Anything, mock.Anything, mock.Anything)
}

func TestAIPromptAction_Execute_TypingErrorIsNonFatal(t *testing.T) {
	api := newTestAPI()
	api.On("PublishUserTyping", testBotUserID, "C1", "").Return(
		mmmodel.NewAppError("publish", "boom", nil, "", 500),
	)

	bc := &mockBridgeClient{agentResponse: "ok"}
	a := NewAIPromptAction(api, bc, testBotUserID)

	ctx := &model.AutomationContext{
		Trigger: model.TriggerData{Channel: &model.SafeChannel{Id: "C1"}},
		Steps:   make(map[string]model.StepOutput),
	}

	output, err := a.Execute(validAIPromptAction(), ctx)
	require.NoError(t, err)
	require.NotNil(t, output)
	assert.Equal(t, "ok", output.Message)
	api.AssertCalled(t, "PublishUserTyping", testBotUserID, "C1", "")
}

func TestAIPromptAction_Execute_TypingStopsAfterCompletion(t *testing.T) {
	api := newTestAPI()
	api.On("PublishUserTyping", testBotUserID, "C1", "").Return(nil)

	block := make(chan struct{})
	started := make(chan struct{})
	bc := &mockBridgeClient{
		agentResponse: "ok",
		agentBlock:    block,
		agentStarted:  started,
	}
	a := NewAIPromptAction(api, bc, testBotUserID)

	ctx := &model.AutomationContext{
		Trigger: model.TriggerData{Channel: &model.SafeChannel{Id: "C1"}},
		Steps:   make(map[string]model.StepOutput),
	}

	done := make(chan error, 1)
	go func() {
		_, err := a.Execute(validAIPromptAction(), ctx)
		done <- err
	}()

	// Wait until the LLM call is in flight; by this point the initial
	// synchronous PublishUserTyping has already fired.
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("AgentCompletion was never entered")
	}

	// Release the LLM call; Execute should return promptly and stop the
	// typing goroutine via defer stopTyping().
	close(block)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Execute did not return after LLM call was released")
	}

	callsAtCompletion := countTypingCalls(api)
	require.Greater(t, callsAtCompletion, 0, "expected at least one typing publish before completion")
	// Sleep past one full republish interval so a leaked goroutine would fire
	// at least once more, then assert the count is stable.
	time.Sleep(typingRepublishInterval + 100*time.Millisecond)
	assert.Equal(t, callsAtCompletion, countTypingCalls(api),
		"PublishUserTyping should not be called after Execute returns")
}

func countTypingCalls(api *plugintest.API) int {
	n := 0
	for _, c := range api.Calls {
		if c.Method == "PublishUserTyping" {
			n++
		}
	}
	return n
}

func TestAIPromptAction_Execute_UsesCustomBotFromNextSendMessage(t *testing.T) {
	const customBotID = "custom-bot-id"
	api := newTestAPI()
	api.On("PublishUserTyping", customBotID, "C1", "").Return(nil)

	bc := &mockBridgeClient{agentResponse: "ok"}
	a := NewAIPromptAction(api, bc, testBotUserID)

	act := validAIPromptAction()
	ctx := &model.AutomationContext{
		Trigger: model.TriggerData{Channel: &model.SafeChannel{Id: "C1"}},
		Steps:   make(map[string]model.StepOutput),
		// Simulate executor: full action list with ai_prompt first so
		// typingBotID can find itself and scan the following send_message.
		Actions: []model.Action{
			*act,
			{
				ID: "send1",
				SendMessage: &model.SendMessageActionConfig{
					ChannelID: "C1",
					Body:      "reply",
					AsBotID:   customBotID,
				},
			},
		},
	}

	_, err := a.Execute(act, ctx)
	require.NoError(t, err)
	api.AssertCalled(t, "PublishUserTyping", customBotID, "C1", "")
	api.AssertNotCalled(t, "PublishUserTyping", testBotUserID, mock.Anything, mock.Anything)
}

func TestAIPromptAction_Execute_PublishesTypingInThreadForRootPostWhenSendMessageReplies(t *testing.T) {
	// User posts a root post; the automation auto-replies into the thread
	// via reply_to_post_id. The typing indicator must follow the reply into
	// the thread (parent = trigger post Id), not stay at channel scope.
	api := newTestAPI()
	api.On("PublishUserTyping", testBotUserID, "C1", "rootp").Return(nil)

	bc := &mockBridgeClient{agentResponse: "ok"}
	a := NewAIPromptAction(api, bc, testBotUserID)

	act := validAIPromptAction()
	ctx := &model.AutomationContext{
		Trigger: model.TriggerData{
			Channel: &model.SafeChannel{Id: "C1"},
			Post:    &model.SafePost{Id: "rootp", ThreadId: "rootp", ChannelId: "C1"},
		},
		Steps: make(map[string]model.StepOutput),
		Actions: []model.Action{
			*act,
			{
				ID: "send1",
				SendMessage: &model.SendMessageActionConfig{
					ChannelID:     "C1",
					ReplyToPostID: "{{.Trigger.Post.ThreadId}}",
					Body:          "reply",
				},
			},
		},
	}

	_, err := a.Execute(act, ctx)
	require.NoError(t, err)
	api.AssertCalled(t, "PublishUserTyping", testBotUserID, "C1", "rootp")
	api.AssertNotCalled(t, "PublishUserTyping", testBotUserID, "C1", "")
}

func TestAIPromptAction_Execute_PublishesTypingAtChannelScopeForRootPostWithoutThreadedReply(t *testing.T) {
	// Root-post trigger with a send_message that posts at channel scope
	// (no reply_to_post_id): typing must stay at channel scope to match.
	api := newTestAPI()
	api.On("PublishUserTyping", testBotUserID, "C1", "").Return(nil)

	bc := &mockBridgeClient{agentResponse: "ok"}
	a := NewAIPromptAction(api, bc, testBotUserID)

	act := validAIPromptAction()
	ctx := &model.AutomationContext{
		Trigger: model.TriggerData{
			Channel: &model.SafeChannel{Id: "C1"},
			Post:    &model.SafePost{Id: "rootp", ThreadId: "rootp", ChannelId: "C1"},
		},
		Steps: make(map[string]model.StepOutput),
		Actions: []model.Action{
			*act,
			{
				ID:          "send1",
				SendMessage: &model.SendMessageActionConfig{ChannelID: "C1", Body: "reply"},
			},
		},
	}

	_, err := a.Execute(act, ctx)
	require.NoError(t, err)
	api.AssertCalled(t, "PublishUserTyping", testBotUserID, "C1", "")
}

func TestAIPromptAction_Execute_TypingScopeFollowsNextSendMessageOnly(t *testing.T) {
	// Two ai_prompt → send_message pairs in one automation. The first
	// send_message posts the first prompt's response at channel scope; only
	// the second send_message replies in a thread. Typing for the first
	// ai_prompt must follow its own send_message (channel scope), not be
	// pulled into the thread by an unrelated later send_message.
	api := newTestAPI()
	api.On("PublishUserTyping", testBotUserID, "C1", "").Return(nil)

	bc := &mockBridgeClient{agentResponse: "ok"}
	a := NewAIPromptAction(api, bc, testBotUserID)

	first := validAIPromptAction()
	ctx := &model.AutomationContext{
		Trigger: model.TriggerData{
			Channel: &model.SafeChannel{Id: "C1"},
			Post:    &model.SafePost{Id: "rootp", ThreadId: "rootp", ChannelId: "C1"},
		},
		Steps: make(map[string]model.StepOutput),
		Actions: []model.Action{
			*first,
			{
				ID:          "send1",
				SendMessage: &model.SendMessageActionConfig{ChannelID: "C1", Body: "channel reply"},
			},
			{ID: "ai2", AIPrompt: &model.AIPromptActionConfig{Prompt: "p", ProviderType: "agent", ProviderID: "ai"}},
			{
				ID: "send2",
				SendMessage: &model.SendMessageActionConfig{
					ChannelID:     "C1",
					ReplyToPostID: "{{.Trigger.Post.ThreadId}}",
					Body:          "thread reply",
				},
			},
		},
	}

	_, err := a.Execute(first, ctx)
	require.NoError(t, err)
	api.AssertCalled(t, "PublishUserTyping", testBotUserID, "C1", "")
	api.AssertNotCalled(t, "PublishUserTyping", testBotUserID, "C1", "rootp")
}

func TestAIPromptAction_Execute_TypingBotIDFollowsNextSendMessageOnly(t *testing.T) {
	// First send_message has no AsBotID (default bot); a later send_message
	// uses a custom bot. typingUserID must use the default for the first
	// ai_prompt, not the unrelated later send_message's bot.
	api := newTestAPI()
	api.On("PublishUserTyping", testBotUserID, "C1", "").Return(nil)

	bc := &mockBridgeClient{agentResponse: "ok"}
	a := NewAIPromptAction(api, bc, testBotUserID)

	first := validAIPromptAction()
	ctx := &model.AutomationContext{
		Trigger: model.TriggerData{Channel: &model.SafeChannel{Id: "C1"}},
		Steps:   make(map[string]model.StepOutput),
		Actions: []model.Action{
			*first,
			{
				ID:          "send1",
				SendMessage: &model.SendMessageActionConfig{ChannelID: "C1", Body: "default bot"},
			},
			{ID: "ai2", AIPrompt: &model.AIPromptActionConfig{Prompt: "p", ProviderType: "agent", ProviderID: "ai"}},
			{
				ID:          "send2",
				SendMessage: &model.SendMessageActionConfig{ChannelID: "C1", Body: "custom bot", AsBotID: "later-bot"},
			},
		},
	}

	_, err := a.Execute(first, ctx)
	require.NoError(t, err)
	api.AssertCalled(t, "PublishUserTyping", testBotUserID, "C1", "")
	api.AssertNotCalled(t, "PublishUserTyping", "later-bot", mock.Anything, mock.Anything)
}

func TestAIPromptAction_Execute_FallsBackToDefaultBotWhenNoCustomBot(t *testing.T) {
	api := newTestAPI()
	api.On("PublishUserTyping", testBotUserID, "C1", "").Return(nil)

	bc := &mockBridgeClient{agentResponse: "ok"}
	a := NewAIPromptAction(api, bc, testBotUserID)

	act := validAIPromptAction()
	ctx := &model.AutomationContext{
		Trigger: model.TriggerData{Channel: &model.SafeChannel{Id: "C1"}},
		Steps:   make(map[string]model.StepOutput),
		// Next send_message has no AsBotID — should fall back to default bot.
		Actions: []model.Action{
			*act,
			{
				ID:          "send1",
				SendMessage: &model.SendMessageActionConfig{ChannelID: "C1", Body: "reply"},
			},
		},
	}

	_, err := a.Execute(act, ctx)
	require.NoError(t, err)
	api.AssertCalled(t, "PublishUserTyping", testBotUserID, "C1", "")
}
