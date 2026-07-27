package permissions

import (
	"errors"
	"net/http"
	"testing"

	mmmodel "github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin/plugintest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mattermost/mattermost-plugin-channel-automation/server/model"
)

func aiPromptAutomation(triggerChannelID string, allowedTools []string, guardrailChannelIDs []string) *model.Automation {
	ai := &model.AIPromptActionConfig{
		Prompt:       "do something",
		ProviderType: "agent",
		ProviderID:   "ag1",
		AllowedTools: allowedTools,
	}
	if guardrailChannelIDs != nil {
		channels := make([]model.GuardrailChannel, 0, len(guardrailChannelIDs))
		for _, id := range guardrailChannelIDs {
			channels = append(channels, model.GuardrailChannel{ChannelID: id})
		}
		ai.Guardrails = &model.Guardrails{Channels: channels}
	}
	return &model.Automation{
		CreatedBy: "creator1",
		Trigger:   model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: triggerChannelID}},
		Actions:   []model.Action{{ID: "a1", AIPrompt: ai}},
	}
}

func TestCheckGuardrailsRequired_PublicChannelRequiresGuardrails(t *testing.T) {
	api := &plugintest.API{}
	api.On("GetChannel", "ch-pub").Return(&mmmodel.Channel{Id: "ch-pub", Type: mmmodel.ChannelTypeOpen}, nil)

	f := aiPromptAutomation("ch-pub", []string{"search_posts"}, nil)

	err := CheckGuardrailsRequired(api, f, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "public channel")
}

func TestCheckGuardrailsRequired_NamespacedToolRequiresGuardrails(t *testing.T) {
	api := &plugintest.API{}
	api.On("GetChannel", "ch-pub").Return(&mmmodel.Channel{Id: "ch-pub", Type: mmmodel.ChannelTypeOpen}, nil)

	// A namespaced "mattermost__" tool must be recognized as guardrail-constrained.
	f := aiPromptAutomation("ch-pub", []string{"mattermost__search_posts"}, nil)

	err := CheckGuardrailsRequired(api, f, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "public channel")
}

func TestCheckGuardrailsRequired_PublicChannelGuardrailsSetAllowed(t *testing.T) {
	api := &plugintest.API{}
	api.On("GetChannel", "ch-pub").Return(&mmmodel.Channel{Id: "ch-pub", Type: mmmodel.ChannelTypeOpen}, nil).Maybe()

	f := aiPromptAutomation("ch-pub", []string{"search_posts"}, []string{mmmodel.NewId()})

	err := CheckGuardrailsRequired(api, f, "")
	require.NoError(t, err)
}

func TestCheckGuardrailsRequired_PrivateSingleMemberAllowed(t *testing.T) {
	api := &plugintest.API{}
	api.On("GetChannel", "ch-priv").Return(&mmmodel.Channel{Id: "ch-priv", Type: mmmodel.ChannelTypePrivate}, nil)
	api.On("GetChannelStats", "ch-priv").Return(&mmmodel.ChannelStats{ChannelId: "ch-priv", MemberCount: 1}, nil)

	f := aiPromptAutomation("ch-priv", []string{"search_posts"}, nil)

	err := CheckGuardrailsRequired(api, f, "")
	require.NoError(t, err)
}

func TestCheckGuardrailsRequired_PrivateMultiMemberRequiresGuardrails(t *testing.T) {
	api := &plugintest.API{}
	api.On("GetChannel", "ch-priv").Return(&mmmodel.Channel{Id: "ch-priv", Type: mmmodel.ChannelTypePrivate}, nil)
	api.On("GetChannelStats", "ch-priv").Return(&mmmodel.ChannelStats{ChannelId: "ch-priv", MemberCount: 2}, nil)

	f := aiPromptAutomation("ch-priv", []string{"search_posts"}, nil)

	err := CheckGuardrailsRequired(api, f, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "private channel")
}

func TestCheckGuardrailsRequired_GroupMessageRequiresGuardrails(t *testing.T) {
	api := &plugintest.API{}
	api.On("GetChannel", "ch-gm").Return(&mmmodel.Channel{Id: "ch-gm", Type: mmmodel.ChannelTypeGroup}, nil)

	f := aiPromptAutomation("ch-gm", []string{"search_posts"}, nil)

	err := CheckGuardrailsRequired(api, f, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "group message")
}

func TestCheckGuardrailsRequired_DMWithBotAllowed(t *testing.T) {
	api := &plugintest.API{}
	api.On("GetChannel", "ch-dm").Return(&mmmodel.Channel{Id: "ch-dm", Type: mmmodel.ChannelTypeDirect}, nil)
	api.On("GetChannelMembers", "ch-dm", 0, 2).Return(mmmodel.ChannelMembers{
		{ChannelId: "ch-dm", UserId: "creator1"},
		{ChannelId: "ch-dm", UserId: "botuser"},
	}, nil)
	api.On("GetUser", "botuser").Return(&mmmodel.User{Id: "botuser", IsBot: true}, nil)

	f := aiPromptAutomation("ch-dm", []string{"search_posts"}, nil)

	err := CheckGuardrailsRequired(api, f, "")
	require.NoError(t, err)
}

func TestCheckGuardrailsRequired_DMWithUserRequiresGuardrails(t *testing.T) {
	api := &plugintest.API{}
	api.On("GetChannel", "ch-dm").Return(&mmmodel.Channel{Id: "ch-dm", Type: mmmodel.ChannelTypeDirect}, nil)
	api.On("GetChannelMembers", "ch-dm", 0, 2).Return(mmmodel.ChannelMembers{
		{ChannelId: "ch-dm", UserId: "creator1"},
		{ChannelId: "ch-dm", UserId: "otheruser"},
	}, nil)
	api.On("GetUser", "otheruser").Return(&mmmodel.User{Id: "otheruser", IsBot: false}, nil)

	f := aiPromptAutomation("ch-dm", []string{"search_posts"}, nil)

	err := CheckGuardrailsRequired(api, f, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "direct message")
}

func TestCheckGuardrailsRequired_DMSelfAllowed(t *testing.T) {
	api := &plugintest.API{}
	api.On("GetChannel", "ch-dm").Return(&mmmodel.Channel{Id: "ch-dm", Type: mmmodel.ChannelTypeDirect}, nil)
	api.On("GetChannelMembers", "ch-dm", 0, 2).Return(mmmodel.ChannelMembers{
		{ChannelId: "ch-dm", UserId: "creator1"},
	}, nil)

	f := aiPromptAutomation("ch-dm", []string{"search_posts"}, nil)

	err := CheckGuardrailsRequired(api, f, "")
	require.NoError(t, err)
}

func TestCheckGuardrailsRequired_ChannelCreatedTriggerAlwaysRequires(t *testing.T) {
	api := &plugintest.API{}

	f := &model.Automation{
		CreatedBy: "creator1",
		Trigger:   model.Trigger{ChannelCreated: &model.ChannelCreatedConfig{TeamID: "team1"}},
		Actions: []model.Action{{ID: "a1", AIPrompt: &model.AIPromptActionConfig{
			Prompt: "x", ProviderType: "agent", ProviderID: "ag1",
			AllowedTools: []string{"search_posts"},
		}}},
	}

	err := CheckGuardrailsRequired(api, f, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "channel_created")
}

func TestCheckGuardrailsRequired_UserJoinedTeamTriggerAlwaysRequires(t *testing.T) {
	api := &plugintest.API{}

	f := &model.Automation{
		CreatedBy: "creator1",
		Trigger:   model.Trigger{UserJoinedTeam: &model.UserJoinedTeamConfig{TeamID: "team1"}},
		Actions: []model.Action{{ID: "a1", AIPrompt: &model.AIPromptActionConfig{
			Prompt: "x", ProviderType: "agent", ProviderID: "ag1",
			AllowedTools: []string{"search_posts"},
		}}},
	}

	err := CheckGuardrailsRequired(api, f, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "user_joined_team")
}

func TestCheckGuardrailsRequired_NoAllowedToolsAllowed(t *testing.T) {
	api := &plugintest.API{}

	f := aiPromptAutomation("ch-pub", nil, nil)

	err := CheckGuardrailsRequired(api, f, "")
	require.NoError(t, err)
	api.AssertNotCalled(t, "GetChannel", "ch-pub")
}

func TestCheckGuardrailsRequired_ExternalOnlyToolsAllowedInPublic(t *testing.T) {
	api := &plugintest.API{}

	f := aiPromptAutomation("ch-pub", []string{"external__search", "mattermost__search_users"}, nil)

	// External and unconstrained embedded tools are not guardrail-constrained,
	// so the context is never classified and no channel lookup occurs.
	err := CheckGuardrailsRequired(api, f, "")
	require.NoError(t, err)
	api.AssertNotCalled(t, "GetChannel", "ch-pub")
}

func TestCheckGuardrailsRequired_UnconstrainedEmbeddedToolsAllowedInPublic(t *testing.T) {
	api := &plugintest.API{}

	f := aiPromptAutomation("ch-pub", []string{"search_users", "list_agents"}, nil)

	err := CheckGuardrailsRequired(api, f, "")
	require.NoError(t, err)
	api.AssertNotCalled(t, "GetChannel", "ch-pub")
}

func TestCheckGuardrailsRequired_MixedToolsRequireGuardrailsInPublic(t *testing.T) {
	api := &plugintest.API{}
	api.On("GetChannel", "ch-pub").Return(&mmmodel.Channel{Id: "ch-pub", Type: mmmodel.ChannelTypeOpen}, nil)

	// One constrained tool alongside external/unconstrained ones still requires guardrails.
	f := aiPromptAutomation("ch-pub", []string{"external__search", "search_posts"}, nil)

	err := CheckGuardrailsRequired(api, f, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "public channel")
}

func TestCheckGuardrailsRequired_MixedNamespacedToolsRequireGuardrailsInPublic(t *testing.T) {
	api := &plugintest.API{}
	api.On("GetChannel", "ch-pub").Return(&mmmodel.Channel{Id: "ch-pub", Type: mmmodel.ChannelTypeOpen}, nil)

	// One constrained tool alongside external/unconstrained ones still requires guardrails.
	f := aiPromptAutomation("ch-pub", []string{"external__search", "mattermost__search_posts"}, nil)

	err := CheckGuardrailsRequired(api, f, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "public channel")
}

func TestCheckGuardrailsRequired_SendMessageOnlyAutomationAllowed(t *testing.T) {
	api := &plugintest.API{}

	f := &model.Automation{
		CreatedBy: "creator1",
		Trigger:   model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch-pub"}},
		Actions: []model.Action{{ID: "a1", SendMessage: &model.SendMessageActionConfig{
			ChannelID: "ch-pub", Body: "hi",
		}}},
	}

	err := CheckGuardrailsRequired(api, f, "")
	require.NoError(t, err)
	api.AssertNotCalled(t, "GetChannel", "ch-pub")
}

func TestCheckGuardrailsRequired_GetChannelServerErrorWraps(t *testing.T) {
	api := &plugintest.API{}
	api.On("GetChannel", "ch-pub").Return(nil, &mmmodel.AppError{
		Message:    "database down",
		StatusCode: http.StatusInternalServerError,
	})

	f := aiPromptAutomation("ch-pub", []string{"search_posts"}, nil)

	err := CheckGuardrailsRequired(api, f, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to verify trigger channel")

	var appErr *mmmodel.AppError
	assert.True(t, errors.As(err, &appErr), "error should wrap AppError for 5xx classification")
}

func TestCheckGuardrailsRequired_GetChannelStatsServerErrorWraps(t *testing.T) {
	api := &plugintest.API{}
	api.On("GetChannel", "ch-priv").Return(&mmmodel.Channel{Id: "ch-priv", Type: mmmodel.ChannelTypePrivate}, nil)
	api.On("GetChannelStats", "ch-priv").Return(nil, &mmmodel.AppError{
		Message:    "stats unavailable",
		StatusCode: http.StatusInternalServerError,
	})

	f := aiPromptAutomation("ch-priv", []string{"search_posts"}, nil)

	err := CheckGuardrailsRequired(api, f, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to verify trigger channel members")

	var appErr *mmmodel.AppError
	assert.True(t, errors.As(err, &appErr), "error should wrap AppError for 5xx classification")
}

func TestCheckGuardrailsRequired_GetChannelMembersServerErrorWraps(t *testing.T) {
	api := &plugintest.API{}
	api.On("GetChannel", "ch-dm").Return(&mmmodel.Channel{Id: "ch-dm", Type: mmmodel.ChannelTypeDirect}, nil)
	api.On("GetChannelMembers", "ch-dm", 0, 2).Return(mmmodel.ChannelMembers(nil), &mmmodel.AppError{
		Message:    "members lookup failed",
		StatusCode: http.StatusInternalServerError,
	})

	f := aiPromptAutomation("ch-dm", []string{"search_posts"}, nil)

	err := CheckGuardrailsRequired(api, f, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to verify DM members")

	var appErr *mmmodel.AppError
	assert.True(t, errors.As(err, &appErr), "error should wrap AppError for 5xx classification")
}

func exemptActorAutomation(requestAs string, allowedTools, guardrailChannelIDs []string) *model.Automation {
	f := aiPromptAutomation("ch-priv", allowedTools, guardrailChannelIDs)
	f.Actions[0].AIPrompt.RequestAs = requestAs
	return f
}

// exemptPrivateAPI mocks a single-member private trigger channel — the context
// CheckGuardrailsRequired treats as exempt, so the exempt-triggerer check runs.
func exemptPrivateAPI() *plugintest.API {
	api := &plugintest.API{}
	api.On("GetChannel", "ch-priv").Return(&mmmodel.Channel{Id: "ch-priv", Type: mmmodel.ChannelTypePrivate}, nil).Maybe()
	api.On("GetChannelStats", "ch-priv").Return(&mmmodel.ChannelStats{ChannelId: "ch-priv", MemberCount: 1}, nil).Maybe()
	return api
}

func TestCheckGuardrailsRequired_ExemptForeignTriggererRejected(t *testing.T) {
	api := exemptPrivateAPI()
	f := exemptActorAutomation(model.AIPromptRequestAsTriggerer, []string{"search_posts"}, nil)

	err := CheckGuardrailsRequired(api, f, "intruder")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "can only run as the automation creator")
}

func TestCheckGuardrailsRequired_ExemptCreatorTriggererAllowed(t *testing.T) {
	api := exemptPrivateAPI()
	f := exemptActorAutomation(model.AIPromptRequestAsTriggerer, []string{"search_posts"}, nil)

	err := CheckGuardrailsRequired(api, f, "creator1")
	require.NoError(t, err)
}

func TestCheckGuardrailsRequired_ExemptRequestAsCreatorAllowed(t *testing.T) {
	api := exemptPrivateAPI()
	f := exemptActorAutomation(model.AIPromptRequestAsCreator, []string{"search_posts"}, nil)

	// request_as=creator resolves to the creator regardless of the queued triggerer.
	err := CheckGuardrailsRequired(api, f, "intruder")
	require.NoError(t, err)
}

func TestCheckGuardrailsRequired_ExemptExternalToolsAllowed(t *testing.T) {
	api := exemptPrivateAPI()
	f := exemptActorAutomation(model.AIPromptRequestAsTriggerer, []string{"external__search", "search_users"}, nil)

	// No guardrail-constrained Mattermost tool, so neither guardrails nor the
	// triggerer rule apply.
	err := CheckGuardrailsRequired(api, f, "intruder")
	require.NoError(t, err)
}

func TestCheckGuardrailsRequired_ExemptGuardrailsSkip(t *testing.T) {
	api := exemptPrivateAPI()
	f := exemptActorAutomation(model.AIPromptRequestAsTriggerer, []string{"search_posts"}, []string{mmmodel.NewId()})

	// Guardrailed actions are governed by the hooks, not this exempt-context rule.
	err := CheckGuardrailsRequired(api, f, "intruder")
	require.NoError(t, err)
}

func TestCheckGuardrailsRequired_ExemptEmptyTriggererAllowed(t *testing.T) {
	api := exemptPrivateAPI()
	f := exemptActorAutomation(model.AIPromptRequestAsTriggerer, []string{"search_posts"}, nil)

	// No triggering user (create/update path, schedule) resolves to the creator.
	err := CheckGuardrailsRequired(api, f, "")
	require.NoError(t, err)
}

func TestCheckGuardrailsRequired_GetUserServerErrorWraps(t *testing.T) {
	api := &plugintest.API{}
	api.On("GetChannel", "ch-dm").Return(&mmmodel.Channel{Id: "ch-dm", Type: mmmodel.ChannelTypeDirect}, nil)
	api.On("GetChannelMembers", "ch-dm", 0, 2).Return(mmmodel.ChannelMembers{
		{ChannelId: "ch-dm", UserId: "creator1"},
		{ChannelId: "ch-dm", UserId: "otheruser"},
	}, nil)
	api.On("GetUser", "otheruser").Return(nil, &mmmodel.AppError{
		Message:    "user lookup failed",
		StatusCode: http.StatusInternalServerError,
	})

	f := aiPromptAutomation("ch-dm", []string{"search_posts"}, nil)

	err := CheckGuardrailsRequired(api, f, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to verify DM participant")

	var appErr *mmmodel.AppError
	assert.True(t, errors.As(err, &appErr), "error should wrap AppError for 5xx classification")
}
