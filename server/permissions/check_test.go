package permissions

import (
	"errors"
	"net/http"
	"testing"

	mmmodel "github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin/plugintest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/mattermost/mattermost-plugin-channel-automation/server/model"
)

func TestCheckAutomationPermissions_SystemAdminBypassesTeamScopedTriggers(t *testing.T) {
	api := &plugintest.API{}
	api.On("HasPermissionTo", "admin1", mmmodel.PermissionManageSystem).Return(true)

	f := &model.Automation{
		Trigger: model.Trigger{ChannelCreated: &model.ChannelCreatedConfig{TeamID: "team1"}},
	}

	err := CheckAutomationPermissions(api, "admin1", f)
	require.NoError(t, err)
}

func TestCheckAutomationPermissions_SystemAdminMemberAllowed(t *testing.T) {
	api := &plugintest.API{}
	api.On("HasPermissionTo", "admin1", mmmodel.PermissionManageSystem).Return(true)
	api.On("GetChannel", "ch1").Return(
		&mmmodel.Channel{Id: "ch1", TeamId: "team1", Type: mmmodel.ChannelTypeOpen}, nil,
	)
	api.On("GetChannelMember", "ch1", "admin1").Return(&mmmodel.ChannelMember{}, nil)

	f := &model.Automation{
		Trigger: model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch1"}},
		Actions: []model.Action{
			{ID: "a1", SendMessage: &model.SendMessageActionConfig{ChannelID: "ch1", Body: "hi"}},
		},
	}

	err := CheckAutomationPermissions(api, "admin1", f)
	require.NoError(t, err)
	api.AssertExpectations(t)
}

func TestCheckAutomationPermissions_SysadminNotMemberDenied(t *testing.T) {
	api := &plugintest.API{}
	api.On("HasPermissionTo", "admin1", mmmodel.PermissionManageSystem).Return(true)
	api.On("GetChannel", "ch1").Return(
		&mmmodel.Channel{Id: "ch1", TeamId: "team1", Type: mmmodel.ChannelTypeOpen}, nil,
	)
	api.On("GetChannelMember", "ch1", "admin1").Return(nil, &mmmodel.AppError{
		Message:    "channel member not found",
		StatusCode: http.StatusNotFound,
	})

	f := &model.Automation{
		Trigger: model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch1"}},
	}

	err := CheckAutomationPermissions(api, "admin1", f)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "permission to manage")
	api.AssertExpectations(t)
}

func TestCheckAutomationPermissions_ChannelCreated_TeamAdminAllowed(t *testing.T) {
	api := &plugintest.API{}
	api.On("HasPermissionTo", "user1", mmmodel.PermissionManageSystem).Return(false)
	api.On("GetTeam", "team1").Return(&mmmodel.Team{Id: "team1"}, nil)
	api.On("HasPermissionToTeam", "user1", "team1", mmmodel.PermissionManageTeam).Return(true)

	f := &model.Automation{
		Trigger: model.Trigger{ChannelCreated: &model.ChannelCreatedConfig{TeamID: "team1"}},
		Actions: []model.Action{
			{ID: "a1", SendMessage: &model.SendMessageActionConfig{ChannelID: "{{.Trigger.Channel.Id}}", Body: "hi"}},
		},
	}

	err := CheckAutomationPermissions(api, "user1", f)
	require.NoError(t, err)
}

func TestCheckAutomationPermissions_ChannelCreated_NonTeamAdminDenied(t *testing.T) {
	api := &plugintest.API{}
	api.On("HasPermissionTo", "user1", mmmodel.PermissionManageSystem).Return(false)
	api.On("GetTeam", "team1").Return(&mmmodel.Team{Id: "team1"}, nil)
	api.On("HasPermissionToTeam", "user1", "team1", mmmodel.PermissionManageTeam).Return(false)

	f := &model.Automation{
		Trigger: model.Trigger{ChannelCreated: &model.ChannelCreatedConfig{TeamID: "team1"}},
	}

	err := CheckAutomationPermissions(api, "user1", f)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "team admin")
}

func TestCheckAutomationPermissions_ChannelCreated_LiteralChannelSameTeam(t *testing.T) {
	api := &plugintest.API{}
	api.On("HasPermissionTo", "user1", mmmodel.PermissionManageSystem).Return(false)
	api.On("GetTeam", "team1").Return(&mmmodel.Team{Id: "team1"}, nil)
	api.On("HasPermissionToTeam", "user1", "team1", mmmodel.PermissionManageTeam).Return(true)
	api.On("GetChannel", "ch1").Return(&mmmodel.Channel{Id: "ch1", TeamId: "team1"}, nil)

	f := &model.Automation{
		Trigger: model.Trigger{ChannelCreated: &model.ChannelCreatedConfig{TeamID: "team1"}},
		Actions: []model.Action{
			{ID: "a1", SendMessage: &model.SendMessageActionConfig{ChannelID: "ch1", Body: "hi"}},
		},
	}

	err := CheckAutomationPermissions(api, "user1", f)
	require.NoError(t, err)
}

func TestCheckAutomationPermissions_ChannelCreated_LiteralChannelWrongTeam(t *testing.T) {
	api := &plugintest.API{}
	api.On("HasPermissionTo", "user1", mmmodel.PermissionManageSystem).Return(false)
	api.On("GetTeam", "team1").Return(&mmmodel.Team{Id: "team1"}, nil)
	api.On("HasPermissionToTeam", "user1", "team1", mmmodel.PermissionManageTeam).Return(true)
	api.On("GetChannel", "ch-other").Return(&mmmodel.Channel{Id: "ch-other", TeamId: "team2"}, nil)

	f := &model.Automation{
		Trigger: model.Trigger{ChannelCreated: &model.ChannelCreatedConfig{TeamID: "team1"}},
		Actions: []model.Action{
			{ID: "a1", SendMessage: &model.SendMessageActionConfig{ChannelID: "ch-other", Body: "hi"}},
		},
	}

	err := CheckAutomationPermissions(api, "user1", f)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not belong to the team")
}

func TestCheckAutomationPermissions_ChannelCreated_GetTeamServerError(t *testing.T) {
	api := &plugintest.API{}
	api.On("HasPermissionTo", "user1", mmmodel.PermissionManageSystem).Return(false)
	api.On("GetTeam", "team1").Return(nil, &mmmodel.AppError{
		Message:    "database connection lost",
		StatusCode: http.StatusInternalServerError,
	})

	f := &model.Automation{
		Trigger: model.Trigger{ChannelCreated: &model.ChannelCreatedConfig{TeamID: "team1"}},
	}

	err := CheckAutomationPermissions(api, "user1", f)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to verify team")

	var appErr *mmmodel.AppError
	assert.True(t, errors.As(err, &appErr), "error should wrap AppError for 5xx classification")
}

func TestCheckAutomationPermissions_ChannelCreated_GetTeamNotFound(t *testing.T) {
	api := &plugintest.API{}
	api.On("HasPermissionTo", "user1", mmmodel.PermissionManageSystem).Return(false)
	api.On("GetTeam", "bad-team").Return(nil, &mmmodel.AppError{
		Message:    "team not found",
		StatusCode: http.StatusNotFound,
	})

	f := &model.Automation{
		Trigger: model.Trigger{ChannelCreated: &model.ChannelCreatedConfig{TeamID: "bad-team"}},
	}

	err := CheckAutomationPermissions(api, "user1", f)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found or not accessible")
}

func TestCheckAutomationPermissions_ChannelCreated_GetChannelServerError(t *testing.T) {
	api := &plugintest.API{}
	api.On("HasPermissionTo", "user1", mmmodel.PermissionManageSystem).Return(false)
	api.On("GetTeam", "team1").Return(&mmmodel.Team{Id: "team1"}, nil)
	api.On("HasPermissionToTeam", "user1", "team1", mmmodel.PermissionManageTeam).Return(true)
	api.On("GetChannel", "ch1").Return(nil, &mmmodel.AppError{
		Message:    "database error",
		StatusCode: http.StatusInternalServerError,
	})

	f := &model.Automation{
		Trigger: model.Trigger{ChannelCreated: &model.ChannelCreatedConfig{TeamID: "team1"}},
		Actions: []model.Action{
			{ID: "a1", SendMessage: &model.SendMessageActionConfig{ChannelID: "ch1", Body: "hi"}},
		},
	}

	err := CheckAutomationPermissions(api, "user1", f)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to verify channel team membership")

	var appErr *mmmodel.AppError
	assert.True(t, errors.As(err, &appErr), "error should wrap AppError for 5xx classification")
}

func TestCheckAutomationPermissions_ChannelCreated_GetChannelNotFound(t *testing.T) {
	api := &plugintest.API{}
	api.On("HasPermissionTo", "user1", mmmodel.PermissionManageSystem).Return(false)
	api.On("GetTeam", "team1").Return(&mmmodel.Team{Id: "team1"}, nil)
	api.On("HasPermissionToTeam", "user1", "team1", mmmodel.PermissionManageTeam).Return(true)
	api.On("GetChannel", "ch-gone").Return(nil, &mmmodel.AppError{
		Message:    "channel not found",
		StatusCode: http.StatusNotFound,
	})

	f := &model.Automation{
		Trigger: model.Trigger{ChannelCreated: &model.ChannelCreatedConfig{TeamID: "team1"}},
		Actions: []model.Action{
			{ID: "a1", SendMessage: &model.SendMessageActionConfig{ChannelID: "ch-gone", Body: "hi"}},
		},
	}

	err := CheckAutomationPermissions(api, "user1", f)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found or not accessible")
}

func TestCheckGuardrailChannelPermissions_SystemAdminDeniedPublicChannelWithoutMembership(t *testing.T) {
	// A sysadmin implicitly satisfies PermissionReadChannel everywhere, but must
	// be an actual member to reference a channel in guardrails — otherwise they
	// could scope guardrails at a public channel in a team they never joined and
	// extract its contents via the agent. No PermissionReadChannel shortcut.
	api := &plugintest.API{}
	api.On("GetChannel", "ch-pub").Return(&mmmodel.Channel{Id: "ch-pub", Type: mmmodel.ChannelTypeOpen}, nil)
	api.On("GetChannelMember", "ch-pub", "admin1").Return(nil, &mmmodel.AppError{
		Message:    "not a member",
		StatusCode: http.StatusNotFound,
	})

	f := &model.Automation{
		Actions: []model.Action{
			{ID: "a1", AIPrompt: &model.AIPromptActionConfig{
				Prompt: "p", ProviderType: "agent", ProviderID: "bot",
				Guardrails: &model.Guardrails{Channels: []model.GuardrailChannel{{ChannelID: "ch-pub"}}},
			}},
		},
	}
	err := CheckGuardrailChannelPermissions(api, "admin1", f)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "do not have permission to read")
	api.AssertNotCalled(t, "HasPermissionToChannel", "admin1", "ch-pub", mmmodel.PermissionReadChannel)
	api.AssertExpectations(t)
}

func TestCheckGuardrailChannelPermissions_SystemAdminDeniedPrivateChannelWithoutMembership(t *testing.T) {
	api := &plugintest.API{}
	api.On("GetChannel", "ch-priv").Return(&mmmodel.Channel{Id: "ch-priv", Type: mmmodel.ChannelTypePrivate}, nil)
	api.On("GetChannelMember", "ch-priv", "admin1").Return(nil, &mmmodel.AppError{
		Message:    "not a member",
		StatusCode: http.StatusNotFound,
	})

	f := &model.Automation{
		Actions: []model.Action{
			{ID: "a1", AIPrompt: &model.AIPromptActionConfig{
				Prompt: "p", ProviderType: "agent", ProviderID: "bot",
				Guardrails: &model.Guardrails{Channels: []model.GuardrailChannel{{ChannelID: "ch-priv"}}},
			}},
		},
	}
	err := CheckGuardrailChannelPermissions(api, "admin1", f)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "do not have permission to read")
	api.AssertNotCalled(t, "HasPermissionToChannel", "admin1", "ch-priv", mmmodel.PermissionReadChannel)
	api.AssertExpectations(t)
}

func TestCheckGuardrailChannelPermissions_MemberAllowed(t *testing.T) {
	api := &plugintest.API{}
	api.On("GetChannel", "ch-priv").Return(&mmmodel.Channel{Id: "ch-priv", Type: mmmodel.ChannelTypePrivate}, nil)
	api.On("GetChannelMember", "ch-priv", "user1").Return(&mmmodel.ChannelMember{ChannelId: "ch-priv", UserId: "user1"}, nil)

	f := &model.Automation{
		Actions: []model.Action{
			{ID: "a1", AIPrompt: &model.AIPromptActionConfig{
				Prompt: "p", ProviderType: "agent", ProviderID: "bot",
				Guardrails: &model.Guardrails{Channels: []model.GuardrailChannel{{ChannelID: "ch-priv"}}},
			}},
		},
	}
	require.NoError(t, CheckGuardrailChannelPermissions(api, "user1", f))
	api.AssertExpectations(t)
}

func TestCheckGuardrailChannelPermissions_GetChannelMemberServerError(t *testing.T) {
	api := &plugintest.API{}
	api.On("GetChannel", "ch-priv").Return(&mmmodel.Channel{Id: "ch-priv", Type: mmmodel.ChannelTypePrivate}, nil)
	api.On("GetChannelMember", "ch-priv", "user1").Return(nil, &mmmodel.AppError{
		Message:    "membership lookup failed",
		StatusCode: http.StatusInternalServerError,
	})

	f := &model.Automation{
		Actions: []model.Action{
			{ID: "a1", AIPrompt: &model.AIPromptActionConfig{
				Prompt: "p", ProviderType: "agent", ProviderID: "bot",
				Guardrails: &model.Guardrails{Channels: []model.GuardrailChannel{{ChannelID: "ch-priv"}}},
			}},
		},
	}
	err := CheckGuardrailChannelPermissions(api, "user1", f)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to verify guardrail channel membership")

	var appErr *mmmodel.AppError
	assert.True(t, errors.As(err, &appErr), "error should wrap AppError for 5xx classification")
	api.AssertExpectations(t)
}

func TestCheckGuardrailChannelPermissions_NoAIPromptOrGuardrails(t *testing.T) {
	api := &plugintest.API{}

	f := &model.Automation{
		Actions: []model.Action{
			{ID: "a1", SendMessage: &model.SendMessageActionConfig{ChannelID: "ch1", Body: "hi"}},
			{ID: "a2", AIPrompt: &model.AIPromptActionConfig{Prompt: "p", ProviderType: "agent", ProviderID: "bot"}},
		},
	}
	require.NoError(t, CheckGuardrailChannelPermissions(api, "user1", f))
	api.AssertExpectations(t)
}

func TestCheckGuardrailChannelPermissions_AllAccessible(t *testing.T) {
	api := &plugintest.API{}
	api.On("GetChannel", "ch1").Return(&mmmodel.Channel{Id: "ch1", Type: mmmodel.ChannelTypeOpen}, nil)
	api.On("GetChannel", "ch2").Return(&mmmodel.Channel{Id: "ch2", Type: mmmodel.ChannelTypeOpen}, nil)
	api.On("GetChannelMember", "ch1", "user1").Return(&mmmodel.ChannelMember{ChannelId: "ch1", UserId: "user1"}, nil)
	api.On("GetChannelMember", "ch2", "user1").Return(&mmmodel.ChannelMember{ChannelId: "ch2", UserId: "user1"}, nil)

	f := &model.Automation{
		Actions: []model.Action{
			{ID: "a1", AIPrompt: &model.AIPromptActionConfig{
				Prompt: "p", ProviderType: "agent", ProviderID: "bot",
				Guardrails: &model.Guardrails{Channels: []model.GuardrailChannel{
					{ChannelID: "ch1"}, {ChannelID: "ch2"},
				}},
			}},
		},
	}
	require.NoError(t, CheckGuardrailChannelPermissions(api, "user1", f))
	api.AssertExpectations(t)
}

func TestCheckGuardrailChannelPermissions_NonMemberDenied(t *testing.T) {
	api := &plugintest.API{}
	api.On("GetChannel", "ch-secret").Return(&mmmodel.Channel{Id: "ch-secret", Type: mmmodel.ChannelTypeOpen}, nil)
	api.On("GetChannelMember", "ch-secret", "user1").Return(nil, &mmmodel.AppError{
		Message:    "not a member",
		StatusCode: http.StatusNotFound,
	})

	f := &model.Automation{
		Actions: []model.Action{
			{ID: "a1", AIPrompt: &model.AIPromptActionConfig{
				Prompt: "p", ProviderType: "agent", ProviderID: "bot",
				Guardrails: &model.Guardrails{Channels: []model.GuardrailChannel{{ChannelID: "ch-secret"}}},
			}},
		},
	}
	err := CheckGuardrailChannelPermissions(api, "user1", f)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "do not have permission to read")
	api.AssertExpectations(t)
}

func TestCheckGuardrailChannelPermissions_GetChannelServerError(t *testing.T) {
	api := &plugintest.API{}
	api.On("GetChannel", "ch1").Return(nil, &mmmodel.AppError{
		Message:    "database error",
		StatusCode: http.StatusInternalServerError,
	})

	f := &model.Automation{
		Actions: []model.Action{
			{ID: "a1", AIPrompt: &model.AIPromptActionConfig{
				Prompt: "p", ProviderType: "agent", ProviderID: "bot",
				Guardrails: &model.Guardrails{Channels: []model.GuardrailChannel{{ChannelID: "ch1"}}},
			}},
		},
	}
	err := CheckGuardrailChannelPermissions(api, "user1", f)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to verify guardrail channel")

	var appErr *mmmodel.AppError
	assert.True(t, errors.As(err, &appErr), "error should wrap AppError for 5xx classification")
	api.AssertExpectations(t)
}

func TestCheckGuardrailChannelPermissions_GetChannelNotFoundDenied(t *testing.T) {
	api := &plugintest.API{}
	api.On("GetChannel", "ch-gone").Return(nil, &mmmodel.AppError{
		Message:    "channel not found",
		StatusCode: http.StatusNotFound,
	})

	f := &model.Automation{
		Actions: []model.Action{
			{ID: "a1", AIPrompt: &model.AIPromptActionConfig{
				Prompt: "p", ProviderType: "agent", ProviderID: "bot",
				Guardrails: &model.Guardrails{Channels: []model.GuardrailChannel{{ChannelID: "ch-gone"}}},
			}},
		},
	}
	err := CheckGuardrailChannelPermissions(api, "user1", f)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "do not have permission to read")
	api.AssertExpectations(t)
}

func TestCheckGuardrailChannelPermissions_DuplicateChannelsDeduped(t *testing.T) {
	api := &plugintest.API{}
	api.On("GetChannel", "ch1").Return(&mmmodel.Channel{Id: "ch1", Type: mmmodel.ChannelTypeOpen}, nil).Once()
	api.On("GetChannelMember", "ch1", "user1").Return(&mmmodel.ChannelMember{ChannelId: "ch1", UserId: "user1"}, nil).Once()

	f := &model.Automation{
		Actions: []model.Action{
			{ID: "a1", AIPrompt: &model.AIPromptActionConfig{
				Prompt: "p", ProviderType: "agent", ProviderID: "bot",
				Guardrails: &model.Guardrails{Channels: []model.GuardrailChannel{{ChannelID: "ch1"}}},
			}},
			{ID: "a2", AIPrompt: &model.AIPromptActionConfig{
				Prompt: "p2", ProviderType: "agent", ProviderID: "bot",
				Guardrails: &model.Guardrails{Channels: []model.GuardrailChannel{{ChannelID: "ch1"}}},
			}},
		},
	}
	require.NoError(t, CheckGuardrailChannelPermissions(api, "user1", f))
	api.AssertExpectations(t)
}

func TestCheckAutomationPermissions_UserJoinedTeam_TeamAdminAllowed(t *testing.T) {
	api := &plugintest.API{}
	api.On("HasPermissionTo", "user1", mmmodel.PermissionManageSystem).Return(false)
	api.On("GetTeam", "team1").Return(&mmmodel.Team{Id: "team1"}, nil)
	api.On("HasPermissionToTeam", "user1", "team1", mmmodel.PermissionManageTeam).Return(true)

	f := &model.Automation{
		Trigger: model.Trigger{UserJoinedTeam: &model.UserJoinedTeamConfig{TeamID: "team1"}},
		Actions: []model.Action{
			{ID: "a1", SendMessage: &model.SendMessageActionConfig{ChannelID: "{{.Trigger.Team.DefaultChannelId}}", Body: "welcome"}},
		},
	}

	err := CheckAutomationPermissions(api, "user1", f)
	require.NoError(t, err)
}

func TestCheckAutomationPermissions_UserJoinedTeam_NonTeamAdminDenied(t *testing.T) {
	api := &plugintest.API{}
	api.On("HasPermissionTo", "user1", mmmodel.PermissionManageSystem).Return(false)
	api.On("GetTeam", "team1").Return(&mmmodel.Team{Id: "team1"}, nil)
	api.On("HasPermissionToTeam", "user1", "team1", mmmodel.PermissionManageTeam).Return(false)

	f := &model.Automation{
		Trigger: model.Trigger{UserJoinedTeam: &model.UserJoinedTeamConfig{TeamID: "team1"}},
	}

	err := CheckAutomationPermissions(api, "user1", f)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "team admin")
}

func TestCheckAutomationPermissions_UserJoinedTeam_GetTeamServerError(t *testing.T) {
	api := &plugintest.API{}
	api.On("HasPermissionTo", "user1", mmmodel.PermissionManageSystem).Return(false)
	api.On("GetTeam", "team1").Return(nil, &mmmodel.AppError{
		Message:    "database connection lost",
		StatusCode: http.StatusInternalServerError,
	})

	f := &model.Automation{
		Trigger: model.Trigger{UserJoinedTeam: &model.UserJoinedTeamConfig{TeamID: "team1"}},
	}

	err := CheckAutomationPermissions(api, "user1", f)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to verify team")

	var appErr *mmmodel.AppError
	assert.True(t, errors.As(err, &appErr), "error should wrap AppError for 5xx classification")
}

func TestCheckAutomationPermissions_UserJoinedTeam_GetTeamNotFound(t *testing.T) {
	api := &plugintest.API{}
	api.On("HasPermissionTo", "user1", mmmodel.PermissionManageSystem).Return(false)
	api.On("GetTeam", "bad-team").Return(nil, &mmmodel.AppError{
		Message:    "team not found",
		StatusCode: http.StatusNotFound,
	})

	f := &model.Automation{
		Trigger: model.Trigger{UserJoinedTeam: &model.UserJoinedTeamConfig{TeamID: "bad-team"}},
	}

	err := CheckAutomationPermissions(api, "user1", f)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found or not accessible")
}

func TestCheckAutomationPermissions_UserJoinedTeam_NoTeamIDs_RequiresSysAdmin(t *testing.T) {
	api := &plugintest.API{}
	api.On("HasPermissionTo", "user1", mmmodel.PermissionManageSystem).Return(false)

	f := &model.Automation{
		Trigger: model.Trigger{UserJoinedTeam: &model.UserJoinedTeamConfig{TeamID: ""}},
	}

	err := CheckAutomationPermissions(api, "user1", f)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "system admin")
}

func TestCheckAutomationPermissions_DMParticipantAllowed(t *testing.T) {
	api := &plugintest.API{}
	api.On("HasPermissionTo", "user1", mmmodel.PermissionManageSystem).Return(false)
	api.On("GetChannelMember", "dm1", "user1").Return(
		&mmmodel.ChannelMember{SchemeAdmin: false}, nil,
	)
	api.On("GetChannel", "dm1").Return(
		&mmmodel.Channel{Id: "dm1", Type: mmmodel.ChannelTypeDirect}, nil,
	)

	f := &model.Automation{
		Trigger: model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "dm1"}},
		Actions: []model.Action{
			{ID: "a1", SendMessage: &model.SendMessageActionConfig{ChannelID: "dm1", Body: "hi"}},
		},
	}
	require.NoError(t, CheckAutomationPermissions(api, "user1", f))
	api.AssertExpectations(t)
}

func TestCheckAutomationPermissions_GMParticipantAllowed(t *testing.T) {
	api := &plugintest.API{}
	api.On("HasPermissionTo", "user1", mmmodel.PermissionManageSystem).Return(false)
	api.On("GetChannelMember", "gm1", "user1").Return(
		&mmmodel.ChannelMember{SchemeAdmin: false}, nil,
	)
	api.On("GetChannel", "gm1").Return(
		&mmmodel.Channel{Id: "gm1", Type: mmmodel.ChannelTypeGroup}, nil,
	)

	f := &model.Automation{
		Trigger: model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "gm1"}},
	}
	require.NoError(t, CheckAutomationPermissions(api, "user1", f))
	api.AssertExpectations(t)
}

func TestCheckAutomationPermissions_RegularChannelNonAdminDenied(t *testing.T) {
	api := &plugintest.API{}
	api.On("HasPermissionTo", "user1", mmmodel.PermissionManageSystem).Return(false)
	api.On("GetChannel", "ch1").Return(
		&mmmodel.Channel{Id: "ch1", TeamId: "team1", Type: mmmodel.ChannelTypeOpen}, nil,
	)
	api.On("GetChannelMember", "ch1", "user1").Return(&mmmodel.ChannelMember{}, nil)
	api.On("HasPermissionToChannel", "user1", "ch1", mmmodel.PermissionManageChannelRoles).Return(false)

	f := &model.Automation{
		Trigger: model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch1"}},
	}
	err := CheckAutomationPermissions(api, "user1", f)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "permission to manage")
	api.AssertExpectations(t)
}

func TestCheckAutomationPermissions_RegularChannelTeamAdminAllowed(t *testing.T) {
	api := &plugintest.API{}
	api.On("HasPermissionTo", "user1", mmmodel.PermissionManageSystem).Return(false)
	api.On("GetChannel", "ch1").Return(
		&mmmodel.Channel{Id: "ch1", TeamId: "team1", Type: mmmodel.ChannelTypeOpen}, nil,
	)
	api.On("GetChannelMember", "ch1", "user1").Return(&mmmodel.ChannelMember{}, nil)
	api.On("HasPermissionToChannel", "user1", "ch1", mmmodel.PermissionManageChannelRoles).Return(true)

	f := &model.Automation{
		Trigger: model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch1"}},
		Actions: []model.Action{
			{ID: "a1", SendMessage: &model.SendMessageActionConfig{ChannelID: "ch1", Body: "hi"}},
		},
	}
	require.NoError(t, CheckAutomationPermissions(api, "user1", f))
	api.AssertExpectations(t)
}

func TestCanViewAutomation_SystemAdminBypassesMembership(t *testing.T) {
	api := &plugintest.API{}
	api.On("HasPermissionTo", "admin1", mmmodel.PermissionManageSystem).Return(true)

	f := &model.Automation{
		Trigger: model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch1"}},
	}

	err := CanViewAutomation(api, "admin1", f)
	require.NoError(t, err)
	api.AssertNotCalled(t, "GetChannel", mock.Anything)
	api.AssertNotCalled(t, "GetChannelMember", mock.Anything, mock.Anything)
}

func TestCanViewAutomation_NonAdminUsesManageCheck(t *testing.T) {
	api := &plugintest.API{}
	api.On("HasPermissionTo", "user1", mmmodel.PermissionManageSystem).Return(false)
	api.On("GetChannel", "ch1").Return(
		&mmmodel.Channel{Id: "ch1", TeamId: "team1", Type: mmmodel.ChannelTypeOpen}, nil,
	)
	api.On("GetChannelMember", "ch1", "user1").Return(nil, &mmmodel.AppError{
		Message:    "channel member not found",
		StatusCode: http.StatusNotFound,
	})

	f := &model.Automation{
		Trigger: model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch1"}},
	}

	err := CanViewAutomation(api, "user1", f)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "permission to manage")
	api.AssertExpectations(t)
}

func TestCanEditAutomation_CreatorAllowed(t *testing.T) {
	api := &plugintest.API{}
	api.On("HasPermissionTo", "creator1", mmmodel.PermissionManageSystem).Return(false)

	f := &model.Automation{CreatedBy: "creator1"}
	require.NoError(t, CanEditAutomation(api, "creator1", f))
	api.AssertExpectations(t)
}

func TestCanEditAutomation_SystemAdminAllowed(t *testing.T) {
	api := &plugintest.API{}
	api.On("HasPermissionTo", "admin1", mmmodel.PermissionManageSystem).Return(true)

	f := &model.Automation{CreatedBy: "creator1"}
	require.NoError(t, CanEditAutomation(api, "admin1", f))
	api.AssertExpectations(t)
}

func TestCanEditAutomation_NonCreatorDenied(t *testing.T) {
	api := &plugintest.API{}
	api.On("HasPermissionTo", "user2", mmmodel.PermissionManageSystem).Return(false)

	f := &model.Automation{CreatedBy: "creator1"}
	err := CanEditAutomation(api, "user2", f)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "automation creator or a system admin")
	api.AssertExpectations(t)
}

func TestCanEditAutomation_MissingCreatedByDenied(t *testing.T) {
	api := &plugintest.API{}
	api.On("HasPermissionTo", "user1", mmmodel.PermissionManageSystem).Return(false)

	f := &model.Automation{}
	err := CanEditAutomation(api, "user1", f)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "automation creator or a system admin")
	api.AssertExpectations(t)
}

func TestCanEditAutomation_NilAutomationDenied(t *testing.T) {
	api := &plugintest.API{}
	api.On("HasPermissionTo", "user1", mmmodel.PermissionManageSystem).Return(false)

	err := CanEditAutomation(api, "user1", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "automation creator or a system admin")
	api.AssertExpectations(t)
}

func TestCheckAutomationPermissions_NonChannelCreated_ChannelAdminRequired(t *testing.T) {
	api := &plugintest.API{}
	api.On("HasPermissionTo", "user1", mmmodel.PermissionManageSystem).Return(false)
	api.On("GetChannel", "ch1").Return(
		&mmmodel.Channel{Id: "ch1", TeamId: "team1", Type: mmmodel.ChannelTypeOpen}, nil,
	)
	api.On("GetChannelMember", "ch1", "user1").Return(&mmmodel.ChannelMember{}, nil)
	api.On("HasPermissionToChannel", "user1", "ch1", mmmodel.PermissionManageChannelRoles).Return(true)

	f := &model.Automation{
		Trigger: model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch1"}},
		Actions: []model.Action{
			{ID: "a1", SendMessage: &model.SendMessageActionConfig{ChannelID: "ch1", Body: "hi"}},
		},
	}

	err := CheckAutomationPermissions(api, "user1", f)
	require.NoError(t, err)
}

func TestCheckAutomationPermissions_RegularChannelHasRoleButNotMemberDenied(t *testing.T) {
	api := &plugintest.API{}
	api.On("HasPermissionTo", "user1", mmmodel.PermissionManageSystem).Return(false)
	api.On("GetChannel", "ch1").Return(
		&mmmodel.Channel{Id: "ch1", TeamId: "team1", Type: mmmodel.ChannelTypeOpen}, nil,
	)
	api.On("GetChannelMember", "ch1", "user1").Return(nil, &mmmodel.AppError{
		Message:    "channel member not found",
		StatusCode: http.StatusNotFound,
	})

	f := &model.Automation{
		Trigger: model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch1"}},
	}

	err := CheckAutomationPermissions(api, "user1", f)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "permission to manage")
	api.AssertNotCalled(t, "HasPermissionToChannel", mock.Anything, mock.Anything, mock.Anything)
}

func TestCheckAutomationPermissions_GetChannelMemberServerError(t *testing.T) {
	api := &plugintest.API{}
	api.On("HasPermissionTo", "user1", mmmodel.PermissionManageSystem).Return(false)
	api.On("GetChannel", "ch1").Return(
		&mmmodel.Channel{Id: "ch1", TeamId: "team1", Type: mmmodel.ChannelTypeOpen}, nil,
	)
	api.On("GetChannelMember", "ch1", "user1").Return(nil, &mmmodel.AppError{
		Message:    "database error",
		StatusCode: http.StatusInternalServerError,
	})

	f := &model.Automation{
		Trigger: model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch1"}},
	}

	err := CheckAutomationPermissions(api, "user1", f)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to verify channel permissions")

	var appErr *mmmodel.AppError
	assert.True(t, errors.As(err, &appErr), "error should wrap AppError for 5xx classification")
}
