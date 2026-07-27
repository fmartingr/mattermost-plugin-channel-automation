package permissions

import (
	"fmt"
	"net/http"

	mmmodel "github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"

	"github.com/mattermost/mattermost-plugin-channel-automation/server/automation/hooks"
	"github.com/mattermost/mattermost-plugin-channel-automation/server/model"
)

// CheckGuardrailsRequired rejects automations whose ai_prompt actions have
// allowed_tools but no guardrails when the trigger context is "sensitive":
//
//   - Team-scoped triggers (channel_created, user_joined_team) — the channel
//     the agent will run against is unknown at create time, so guardrails are
//     always required.
//   - Channel-scoped triggers (message_posted, schedule, membership_changed)
//     where the trigger channel is one of:
//   - public channel (Type O)
//   - private channel (Type P) with more than one member
//   - group message (Type G)
//   - direct message (Type D) where the other participant is not a bot
//
// DMs with the bot itself, single-member private channels (just the creator),
// and ai_prompt actions with no allowed_tools are exempt: in those cases there
// are no humans (other than the creator) who could see tool output sourced
// from elsewhere, or no tools that guardrails could meaningfully constrain.
//
// triggererID is the user that fired this specific event, or "" when there is
// none (the create/update path, and schedule/creator-locked triggers). Even in
// an otherwise-exempt context, a queued event can carry a foreign triggerer
// while live membership has fallen back to exempt (e.g. the other member left
// after posting), so an unguarded guardrail-constrained action would still
// borrow the triggerer's access. When the run resolves to someone other than
// the creator, guardrails are treated as required.
//
// This check intentionally runs after the standard authorization checks so it
// cannot be used to probe channel types without already being authorized to
// create automations referencing them.
func CheckGuardrailsRequired(api plugin.API, f *model.Automation, triggererID string) error {
	actionIdx, needs := firstAIPromptNeedingGuardrails(f)
	if !needs {
		return nil
	}
	if err := checkSensitiveTriggerContext(api, f, actionIdx); err != nil {
		return err
	}
	// Context is exempt for the creator; make sure this queued event actually
	// resolves to the creator and not a foreign triggerer.
	return checkExemptTriggerer(f, triggererID)
}

// checkExemptTriggerer rejects an unguarded guardrail-constrained ai_prompt
// whose resolved actor is not the automation creator. Actor resolution mirrors
// ai_prompt.Execute (request_as plus the triggering user) so the two never
// disagree. triggererID is "" when no triggering user exists.
func checkExemptTriggerer(f *model.Automation, triggererID string) error {
	if f == nil {
		return nil
	}
	for i := range f.Actions {
		ai := f.Actions[i].AIPrompt
		if ai == nil {
			continue
		}
		if ai.Guardrails != nil && len(ai.Guardrails.Channels) > 0 {
			continue
		}
		if !hooks.HasGuardrailConstrainedMattermostMCPTool(ai.AllowedTools) {
			continue
		}
		actor := f.CreatedBy
		if ai.RequestAs != model.AIPromptRequestAsCreator && triggererID != "" {
			actor = triggererID
		}
		if actor != f.CreatedBy {
			return fmt.Errorf("action %d: ai_prompt with guardrail-constrained Mattermost tools and no guardrails can only run as the automation creator (resolved actor %q, creator %q)", i, actor, f.CreatedBy)
		}
	}
	return nil
}

// firstAIPromptNeedingGuardrails returns the index of the first ai_prompt
// action that includes a guardrail-constrained Mattermost MCP tool but has no
// guardrails. External tools and unconstrained embedded tools (search_users,
// list_agents) do not require guardrails, so an action using only those is
// skipped.
func firstAIPromptNeedingGuardrails(f *model.Automation) (int, bool) {
	for i := range f.Actions {
		ai := f.Actions[i].AIPrompt
		if ai == nil {
			continue
		}
		if !hooks.HasGuardrailConstrainedMattermostMCPTool(ai.AllowedTools) {
			continue
		}
		if ai.Guardrails != nil && len(ai.Guardrails.Channels) > 0 {
			continue
		}
		return i, true
	}
	return -1, false
}

// checkSensitiveTriggerContext returns a fully-formed error describing why
// guardrails are required for the trigger context, or nil if the context is
// exempt. A non-nil error wrapping an *AppError indicates an infrastructure
// failure that should surface as 500.
func checkSensitiveTriggerContext(api plugin.API, f *model.Automation, actionIdx int) error {
	if f.Trigger.ChannelCreated != nil {
		return fmt.Errorf("action %d: ai_prompt with allowed_tools requires guardrails.channel_ids when the trigger is channel_created (the channel that will fire is unknown at configuration time)", actionIdx)
	}
	if f.Trigger.UserJoinedTeam != nil {
		return fmt.Errorf("action %d: ai_prompt with allowed_tools requires guardrails.channel_ids when the trigger is user_joined_team (the channel the agent will act on is unknown at configuration time)", actionIdx)
	}

	chID := f.TriggerChannelID()
	if chID == "" {
		return nil
	}

	ch, appErr := api.GetChannel(chID)
	if appErr != nil {
		if appErr.StatusCode >= http.StatusInternalServerError {
			return fmt.Errorf("failed to verify trigger channel: %w", appErr)
		}
		return fmt.Errorf("trigger channel %s not found or not accessible", chID)
	}

	switch ch.Type {
	case mmmodel.ChannelTypeOpen:
		return fmt.Errorf("action %d: ai_prompt with allowed_tools requires guardrails.channel_ids when the trigger fires in a public channel", actionIdx)
	case mmmodel.ChannelTypeGroup:
		return fmt.Errorf("action %d: ai_prompt with allowed_tools requires guardrails.channel_ids when the trigger fires in a group message", actionIdx)
	case mmmodel.ChannelTypePrivate:
		stats, appErr := api.GetChannelStats(chID)
		if appErr != nil {
			if appErr.StatusCode >= http.StatusInternalServerError {
				return fmt.Errorf("failed to verify trigger channel members: %w", appErr)
			}
			return fmt.Errorf("trigger channel %s stats not accessible", chID)
		}
		if stats.MemberCount > 1 {
			return fmt.Errorf("action %d: ai_prompt with allowed_tools requires guardrails.channel_ids when the trigger fires in a private channel with more than one member", actionIdx)
		}
		return nil
	case mmmodel.ChannelTypeDirect:
		return checkDMRequiresGuardrails(api, chID, f.CreatedBy, actionIdx)
	}
	return nil
}

// checkDMRequiresGuardrails enforces the rule that DMs with a non-bot user
// require guardrails. Self-DMs and DMs with a bot are exempt. Membership is
// queried via GetChannelMembers so we don't depend on the channel name format.
func checkDMRequiresGuardrails(api plugin.API, channelID, creatorID string, actionIdx int) error {
	// Mattermost guarantees a DM has exactly 2 members, or 1 for a self-DM.
	// Page size 2 is sufficient; anything outside that range indicates a
	// data-integrity bug and we fail closed rather than guess.
	members, appErr := api.GetChannelMembers(channelID, 0, 2)
	if appErr != nil {
		if appErr.StatusCode >= http.StatusInternalServerError {
			return fmt.Errorf("failed to verify DM members: %w", appErr)
		}
		return fmt.Errorf("DM channel %s members not accessible", channelID)
	}

	var otherID string
	for _, m := range members {
		if m.UserId != creatorID {
			otherID = m.UserId
			break
		}
	}
	if otherID == "" {
		// Self-DM: no other participant to protect against.
		return nil
	}

	other, appErr := api.GetUser(otherID)
	if appErr != nil {
		if appErr.StatusCode >= http.StatusInternalServerError {
			return fmt.Errorf("failed to verify DM participant: %w", appErr)
		}
		return fmt.Errorf("DM participant %s not accessible", otherID)
	}
	if other.IsBot {
		return nil
	}
	return fmt.Errorf("action %d: ai_prompt with allowed_tools requires guardrails.channel_ids when the trigger fires in a direct message with another user", actionIdx)
}
