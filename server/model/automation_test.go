package model

import (
	"encoding/json"
	"testing"

	mmmodel "github.com/mattermost/mattermost/server/public/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollectChannelIDs_LiteralChannels(t *testing.T) {
	a := &Automation{
		Trigger: Trigger{MessagePosted: &MessagePostedConfig{ChannelID: "ch1"}},
		Actions: []Action{
			{SendMessage: &SendMessageActionConfig{ChannelID: "ch2"}},
			{SendMessage: &SendMessageActionConfig{ChannelID: "ch3"}},
		},
	}

	ids := CollectChannelIDs(a)
	assert.Equal(t, []string{"ch1", "ch2", "ch3"}, ids)
}

func TestCollectChannelIDs_TemplatedChannelSkipped(t *testing.T) {
	a := &Automation{
		Trigger: Trigger{MessagePosted: &MessagePostedConfig{ChannelID: "ch1"}},
		Actions: []Action{
			{SendMessage: &SendMessageActionConfig{ChannelID: "{{.Trigger.Channel.Id}}"}},
		},
	}

	ids := CollectChannelIDs(a)
	assert.Equal(t, []string{"ch1"}, ids)
}

func TestCollectChannelIDs_DuplicatesRemoved(t *testing.T) {
	a := &Automation{
		Trigger: Trigger{MessagePosted: &MessagePostedConfig{ChannelID: "ch1"}},
		Actions: []Action{
			{SendMessage: &SendMessageActionConfig{ChannelID: "ch1"}},
			{SendMessage: &SendMessageActionConfig{ChannelID: "ch2"}},
			{SendMessage: &SendMessageActionConfig{ChannelID: "ch2"}},
		},
	}

	ids := CollectChannelIDs(a)
	assert.Equal(t, []string{"ch1", "ch2"}, ids)
}

func TestCollectChannelIDs_ScheduleTrigger(t *testing.T) {
	a := &Automation{
		Trigger: Trigger{Schedule: &ScheduleConfig{ChannelID: "ch1", Interval: "1h"}},
		Actions: []Action{
			{SendMessage: &SendMessageActionConfig{ChannelID: "ch2"}},
		},
	}

	ids := CollectChannelIDs(a)
	assert.Equal(t, []string{"ch1", "ch2"}, ids)
}

func TestCollectChannelIDs_ScheduleTriggerDuplicateWithAction(t *testing.T) {
	a := &Automation{
		Trigger: Trigger{Schedule: &ScheduleConfig{ChannelID: "ch1", Interval: "1h"}},
		Actions: []Action{
			{SendMessage: &SendMessageActionConfig{ChannelID: "ch1"}},
		},
	}

	ids := CollectChannelIDs(a)
	assert.Equal(t, []string{"ch1"}, ids)
}

func TestCollectChannelIDs_MembershipChangedTrigger(t *testing.T) {
	a := &Automation{
		Trigger: Trigger{MembershipChanged: &MembershipChangedConfig{ChannelID: "ch1"}},
		Actions: []Action{
			{SendMessage: &SendMessageActionConfig{ChannelID: "ch2"}},
		},
	}

	ids := CollectChannelIDs(a)
	assert.Equal(t, []string{"ch1", "ch2"}, ids)
}

func TestCollectChannelIDs_MembershipChangedDuplicateWithAction(t *testing.T) {
	a := &Automation{
		Trigger: Trigger{MembershipChanged: &MembershipChangedConfig{ChannelID: "ch1"}},
		Actions: []Action{
			{SendMessage: &SendMessageActionConfig{ChannelID: "ch1"}},
		},
	}

	ids := CollectChannelIDs(a)
	assert.Equal(t, []string{"ch1"}, ids)
}

func TestCollectChannelIDs_NoChannels(t *testing.T) {
	a := &Automation{
		Trigger: Trigger{Schedule: &ScheduleConfig{Interval: "1h"}},
		Actions: []Action{
			{AIPrompt: &AIPromptActionConfig{Prompt: "test"}},
		},
	}

	ids := CollectChannelIDs(a)
	assert.Empty(t, ids)
}

func TestCollectChannelIDs_ChannelCreatedTrigger(t *testing.T) {
	a := &Automation{
		Trigger: Trigger{ChannelCreated: &ChannelCreatedConfig{TeamID: "team1"}},
		Actions: []Action{
			{SendMessage: &SendMessageActionConfig{ChannelID: "{{.Trigger.Channel.Id}}", Body: "hello"}},
		},
	}

	ids := CollectChannelIDs(a)
	assert.Empty(t, ids, "channel_created with templated action channels should return no concrete IDs")
}

func TestCollectChannelIDs_AIPromptGuardrailsExcluded(t *testing.T) {
	// Guardrail channels are an LLM read-only allowlist enforced at the hook
	// layer, not channels the automation acts on. They must not appear in
	// CollectChannelIDs, which feeds channel-admin permission checks.
	ch1 := mmmodel.NewId()
	ch2 := mmmodel.NewId()
	a := &Automation{
		Trigger: Trigger{MessagePosted: &MessagePostedConfig{ChannelID: ch1}},
		Actions: []Action{
			{
				ID: "ai1",
				AIPrompt: &AIPromptActionConfig{
					Prompt: "x", ProviderType: "agent", ProviderID: "bot",
					Guardrails: &Guardrails{Channels: []GuardrailChannel{{ChannelID: ch2}}},
				},
			},
		},
	}
	ids := CollectChannelIDs(a)
	assert.Equal(t, []string{ch1}, ids)
}

func TestCollectChannelIDs_ChannelCreatedWithLiteralAction(t *testing.T) {
	a := &Automation{
		Trigger: Trigger{ChannelCreated: &ChannelCreatedConfig{TeamID: "team1"}},
		Actions: []Action{
			{SendMessage: &SendMessageActionConfig{ChannelID: "ch-notify", Body: "hello"}},
		},
	}

	ids := CollectChannelIDs(a)
	assert.Equal(t, []string{"ch-notify"}, ids)
}

func TestAutomationJSON_AIPrompt_AllowedToolsStringArray(t *testing.T) {
	const raw = `{
		"id": "f1",
		"name": "n",
		"enabled": true,
		"trigger": {"schedule": {"channel_id": "c1", "interval": "1h"}},
		"actions": [{
			"id": "a1",
			"ai_prompt": {
				"prompt": "p",
				"provider_type": "agent",
				"provider_id": "bot1",
				"allowed_tools": ["search", "create_post"]
			}
		}],
		"created_at": 0,
		"updated_at": 0,
		"created_by": "u1"
	}`
	var a Automation
	err := json.Unmarshal([]byte(raw), &a)
	require.NoError(t, err)
	require.Len(t, a.Actions, 1)
	require.NotNil(t, a.Actions[0].AIPrompt)
	assert.Equal(t, []string{"search", "create_post"}, a.Actions[0].AIPrompt.AllowedTools)
}

func TestCollectTeamIDs_LiteralTeamID(t *testing.T) {
	a := &Automation{
		Trigger: Trigger{UserJoinedTeam: &UserJoinedTeamConfig{TeamID: "team1"}},
	}
	ids := CollectTeamIDs(a)
	assert.Equal(t, []string{"team1"}, ids)
}

func TestCollectTeamIDs_EmptyTeamID(t *testing.T) {
	a := &Automation{
		Trigger: Trigger{UserJoinedTeam: &UserJoinedTeamConfig{TeamID: ""}},
	}
	ids := CollectTeamIDs(a)
	assert.Nil(t, ids)
}

func TestCollectTeamIDs_TemplatedTeamID(t *testing.T) {
	a := &Automation{
		Trigger: Trigger{UserJoinedTeam: &UserJoinedTeamConfig{TeamID: "{{.SomeVar}}"}},
	}
	ids := CollectTeamIDs(a)
	assert.Nil(t, ids)
}

func TestCollectTeamIDs_NonTeamTrigger(t *testing.T) {
	a := &Automation{
		Trigger: Trigger{MessagePosted: &MessagePostedConfig{ChannelID: "ch1"}},
	}
	ids := CollectTeamIDs(a)
	assert.Nil(t, ids)
}

func TestAutomation_Update_OverlaysMutableFields(t *testing.T) {
	existing := &Automation{
		ID:        "f1",
		Name:      "Original",
		Enabled:   true,
		Trigger:   Trigger{MessagePosted: &MessagePostedConfig{ChannelID: "ch1"}},
		Actions:   []Action{{ID: "a", SendMessage: &SendMessageActionConfig{ChannelID: "ch1", Body: "hi"}}},
		CreatedAt: 1000,
		UpdatedAt: 1000,
		CreatedBy: "user1",
	}

	enabled := false
	existing.Update(&AutomationUpdate{
		Name:    "Renamed",
		Enabled: &enabled,
		Trigger: Trigger{MessagePosted: &MessagePostedConfig{ChannelID: "ch2"}},
		Actions: []Action{{ID: "b", SendMessage: &SendMessageActionConfig{ChannelID: "ch2", Body: "bye"}}},
	})

	assert.Equal(t, "Renamed", existing.Name)
	assert.False(t, existing.Enabled)
	require.NotNil(t, existing.Trigger.MessagePosted)
	assert.Equal(t, "ch2", existing.Trigger.MessagePosted.ChannelID)
	require.Len(t, existing.Actions, 1)
	assert.Equal(t, "b", existing.Actions[0].ID)

	// Immutable fields are untouched.
	assert.Equal(t, "f1", existing.ID)
	assert.Equal(t, int64(1000), existing.CreatedAt)
	assert.Equal(t, int64(1000), existing.UpdatedAt, "Update must not touch UpdatedAt; the caller manages it")
	assert.Equal(t, "user1", existing.CreatedBy)
}

func TestAutomation_Update_NilEnabledPreservesExisting(t *testing.T) {
	existing := &Automation{
		ID:      "f1",
		Name:    "Original",
		Enabled: true,
		Trigger: Trigger{MessagePosted: &MessagePostedConfig{ChannelID: "ch1"}},
		Actions: []Action{{ID: "a", SendMessage: &SendMessageActionConfig{ChannelID: "ch1", Body: "hi"}}},
	}

	existing.Update(&AutomationUpdate{
		Name:    "Renamed",
		Trigger: existing.Trigger,
		Actions: existing.Actions,
		// Enabled intentionally nil.
	})

	assert.True(t, existing.Enabled, "nil Enabled must preserve existing value")
	assert.Equal(t, "Renamed", existing.Name)
}

func TestAutomationUpdate_JSON_DistinguishesAbsentFromFalse(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		var u AutomationUpdate
		require.NoError(t, json.Unmarshal([]byte(`{"name":"n"}`), &u))
		assert.Nil(t, u.Enabled)
	})
	t.Run("explicit false", func(t *testing.T) {
		var u AutomationUpdate
		require.NoError(t, json.Unmarshal([]byte(`{"name":"n","enabled":false}`), &u))
		require.NotNil(t, u.Enabled)
		assert.False(t, *u.Enabled)
	})
	t.Run("explicit true", func(t *testing.T) {
		var u AutomationUpdate
		require.NoError(t, json.Unmarshal([]byte(`{"name":"n","enabled":true}`), &u))
		require.NotNil(t, u.Enabled)
		assert.True(t, *u.Enabled)
	})
}

func TestValidateCreatorLockedRequestAs(t *testing.T) {
	creatorLockedTriggers := []struct {
		name    string
		trigger Trigger
	}{
		{"membership_changed", Trigger{MembershipChanged: &MembershipChangedConfig{ChannelID: "ch1"}}},
		{"user_joined_team", Trigger{UserJoinedTeam: &UserJoinedTeamConfig{TeamID: "team1"}}},
		{"channel_created", Trigger{ChannelCreated: &ChannelCreatedConfig{TeamID: "team1"}}},
	}

	for _, tt := range creatorLockedTriggers {
		t.Run(tt.name+" rejects triggerer", func(t *testing.T) {
			f := &Automation{
				Trigger: tt.trigger,
				Actions: []Action{{ID: "a1", AIPrompt: &AIPromptActionConfig{RequestAs: AIPromptRequestAsTriggerer}}},
			}
			err := ValidateCreatorLockedRequestAs(f)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "request_as")
		})

		t.Run(tt.name+" rejects empty (defaults to triggerer)", func(t *testing.T) {
			f := &Automation{
				Trigger: tt.trigger,
				Actions: []Action{{ID: "a1", AIPrompt: &AIPromptActionConfig{RequestAs: ""}}},
			}
			require.Error(t, ValidateCreatorLockedRequestAs(f))
		})

		t.Run(tt.name+" allows explicit creator", func(t *testing.T) {
			f := &Automation{
				Trigger: tt.trigger,
				Actions: []Action{{ID: "a1", AIPrompt: &AIPromptActionConfig{RequestAs: AIPromptRequestAsCreator}}},
			}
			require.NoError(t, ValidateCreatorLockedRequestAs(f))
		})
	}

	t.Run("ignores send_message actions", func(t *testing.T) {
		f := &Automation{
			Trigger: Trigger{MembershipChanged: &MembershipChangedConfig{ChannelID: "ch1"}},
			Actions: []Action{{ID: "a1", SendMessage: &SendMessageActionConfig{ChannelID: "ch1", Body: "hi"}}},
		}
		require.NoError(t, ValidateCreatorLockedRequestAs(f))
	})

	t.Run("ignores non-locked triggers", func(t *testing.T) {
		f := &Automation{
			Trigger: Trigger{MessagePosted: &MessagePostedConfig{ChannelID: "ch1"}},
			Actions: []Action{{ID: "a1", AIPrompt: &AIPromptActionConfig{RequestAs: AIPromptRequestAsTriggerer}}},
		}
		require.NoError(t, ValidateCreatorLockedRequestAs(f))
	})

	t.Run("nil automation is safe", func(t *testing.T) {
		require.NoError(t, ValidateCreatorLockedRequestAs(nil))
	})
}

func TestResolvesToTriggerer(t *testing.T) {
	tests := []struct {
		triggerType string
		requestAs   string
		want        bool
	}{
		// request_as=creator always resolves to the creator.
		{TriggerTypeMessagePosted, AIPromptRequestAsCreator, false},
		{TriggerTypeSchedule, AIPromptRequestAsCreator, false},
		// message_posted carries a distinct triggering user.
		{TriggerTypeMessagePosted, AIPromptRequestAsTriggerer, true},
		{TriggerTypeMessagePosted, "", true},
		// schedule has no triggering user.
		{TriggerTypeSchedule, AIPromptRequestAsTriggerer, false},
		{TriggerTypeSchedule, "", false},
		// creator-locked triggers are pinned to the creator.
		{TriggerTypeMembershipChanged, AIPromptRequestAsTriggerer, false},
		{TriggerTypeUserJoinedTeam, "", false},
		{TriggerTypeChannelCreated, AIPromptRequestAsTriggerer, false},
	}
	for _, tt := range tests {
		t.Run(tt.triggerType+"/"+tt.requestAs, func(t *testing.T) {
			assert.Equal(t, tt.want, ResolvesToTriggerer(tt.triggerType, tt.requestAs))
		})
	}
}
