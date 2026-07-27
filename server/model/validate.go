package model

import (
	"fmt"
	"regexp"
	"strings"

	mmmodel "github.com/mattermost/mattermost/server/public/model"
)

var actionIDPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

// ValidateTriggerExclusivity checks that exactly one trigger-config pointer
// is set. Per-type field validation lives on each TriggerHandler and is
// dispatched by the automation package's validator.
func ValidateTriggerExclusivity(t *Trigger) error {
	count := 0
	if t.MessagePosted != nil {
		count++
	}
	if t.Schedule != nil {
		count++
	}
	if t.MembershipChanged != nil {
		count++
	}
	if t.ChannelCreated != nil {
		count++
	}
	if t.UserJoinedTeam != nil {
		count++
	}
	if count == 0 {
		return fmt.Errorf("exactly one trigger type must be set")
	}
	if count > 1 {
		return fmt.Errorf("exactly one trigger type must be set, got %d", count)
	}
	return nil
}

// ValidateSendMessageChannel checks that every send_message action in the automation
// targets the same channel that the trigger is bound to. For triggers with a
// channel_id (message_posted, schedule, membership_changed), the action channel
// must be either the literal trigger channel ID or a template containing
// ".Trigger.Channel.Id". For triggers without a bound channel (channel_created,
// user_joined_team), any Go template expression is accepted (e.g.
// "{{.Trigger.Channel.Id}}" or "{{.Trigger.Team.DefaultChannelId}}").
func ValidateSendMessageChannel(automation *Automation) error {
	triggerChannelID := automation.TriggerChannelID()

	for i, act := range automation.Actions {
		if act.SendMessage == nil {
			continue
		}
		chID := act.SendMessage.ChannelID
		if isTriggerChannelTemplate(chID) {
			continue
		}
		// triggerChannelID is empty for triggers that are not tied to a channel
		// (channel_created, user_joined_team). In that case only template
		// expressions are valid — fail early with a clear message.
		if triggerChannelID == "" {
			return fmt.Errorf("action %d: send_message channel_id must use a template expression (e.g. \"{{.Trigger.Channel.Id}}\" or \"{{.Trigger.Team.DefaultChannelId}}\") for this trigger type", i)
		}
		if chID == triggerChannelID {
			continue
		}
		return fmt.Errorf("action %d: send_message channel_id must reference the triggering channel (use %q or the template expression \"{{.Trigger.Channel.Id}}\")", i, triggerChannelID)
	}
	return nil
}

// CreatorLockedTriggerTypes are the trigger types whose ai_prompt actions must
// set request_as to "creator". For these triggers the "triggerer" is an
// incidental subject of the event (joiner/leaver/channel creator), so running
// the AI with their permissions would let the automation borrow that user's
// access and surface it to other members.
var CreatorLockedTriggerTypes = map[string]struct{}{
	TriggerTypeMembershipChanged: {},
	TriggerTypeUserJoinedTeam:    {},
	TriggerTypeChannelCreated:    {},
}

// IsCreatorLockedTrigger reports whether the automation's trigger type requires
// ai_prompt request_as=creator.
func IsCreatorLockedTrigger(automation *Automation) bool {
	if automation == nil {
		return false
	}
	_, ok := CreatorLockedTriggerTypes[automation.Trigger.Type()]
	return ok
}

// ResolvesToTriggerer reports whether an ai_prompt with the given trigger type
// and request_as could run as a user other than the automation creator. This is
// the single source of truth for that question:
//   - request_as "creator" always resolves to the creator.
//   - schedule triggers have no triggering user, so they resolve to the creator.
//   - creator-locked triggers (membership_changed, user_joined_team,
//     channel_created) are pinned to the creator.
//
// Only the remaining trigger types (currently message_posted) carry a distinct
// triggering user the AI can run as. Callers that must forbid borrowing a
// foreign identity (e.g. creator-only tools) gate on this rule.
func ResolvesToTriggerer(triggerType, requestAs string) bool {
	if requestAs == AIPromptRequestAsCreator {
		return false
	}
	if triggerType == TriggerTypeSchedule {
		return false
	}
	if _, locked := CreatorLockedTriggerTypes[triggerType]; locked {
		return false
	}
	return true
}

// ValidateCreatorLockedRequestAs rejects automations whose trigger is
// creator-locked and whose ai_prompt actions do not explicitly set request_as
// to "creator". An empty request_as means "triggerer" and is therefore also
// rejected: "creator" must be explicit so the behavior is never a surprise.
func ValidateCreatorLockedRequestAs(automation *Automation) error {
	if !IsCreatorLockedTrigger(automation) {
		return nil
	}
	triggerType := automation.Trigger.Type()
	for i := range automation.Actions {
		ai := automation.Actions[i].AIPrompt
		if ai == nil {
			continue
		}
		if ai.RequestAs != AIPromptRequestAsCreator {
			return fmt.Errorf("action %d: %s automations must set request_as to %q (the triggerer is an incidental subject of the event, so the AI cannot run with their permissions)", i, triggerType, AIPromptRequestAsCreator)
		}
	}
	return nil
}

// isTriggerChannelTemplate returns true if s is a Go template expression that
// resolves to a channel ID sourced from the trigger. The whole value must be a
// single template expression (leading/trailing whitespace aside) and must
// reference one of the allowlisted channel-bearing fields:
//
//   - {{.Trigger.Channel.Id}}            — the triggering channel
//   - {{.Trigger.Team.DefaultChannelId}} — user_joined_team default channel
//   - {{.Trigger.Post.ChannelId}}        — parent channel of a triggering post
//
// Templates referencing other fields (e.g. {{.Trigger.User.Id}}) or step
// outputs (e.g. {{.Steps.<id>.ChannelID}}) are rejected at create time. Step
// output chaining is not currently supported for send_message channel IDs.
//
// This is a UX guardrail, not a security boundary — CheckAutomationPermissions is
// the actual authorization layer for literal channel IDs. Templates are
// intentionally excluded from permission checks because their values aren't
// known until runtime.
func isTriggerChannelTemplate(s string) bool {
	trimmed := strings.TrimSpace(s)
	if !strings.HasPrefix(trimmed, "{{") || !strings.HasSuffix(trimmed, "}}") {
		return false
	}
	allowed := []string{
		".Trigger.Channel.Id",
		".Trigger.Team.DefaultChannelId",
		".Trigger.Post.ChannelId",
	}
	for _, a := range allowed {
		if strings.Contains(trimmed, a) {
			return true
		}
	}
	return false
}

// ValidateActions validates a list of actions.
// At least one action is required. Each action must have a unique, non-empty ID
// matching the slug pattern (lowercase alphanumeric + hyphens) and exactly one
// action config set. For ai_prompt actions, guardrail consistency is checked.
// Tool-name policy (catalog membership, embedded-server allowlist, disallowed
// tools) is enforced at the API layer against the live bridge tool list — not
// here — because it requires a per-user, per-agent bridge call.
func ValidateActions(actions []Action) error {
	if len(actions) == 0 {
		return fmt.Errorf("at least one action is required")
	}
	seen := make(map[string]struct{}, len(actions))
	for i, a := range actions {
		if a.ID == "" {
			return fmt.Errorf("action %d: id is required", i)
		}
		if !actionIDPattern.MatchString(a.ID) {
			return fmt.Errorf("action %d: id %q is invalid (must be lowercase alphanumeric with hyphens, e.g. \"send-greeting\")", i, a.ID)
		}
		if _, ok := seen[a.ID]; ok {
			return fmt.Errorf("action %d: duplicate id %q", i, a.ID)
		}
		seen[a.ID] = struct{}{}
		configCount := 0
		if a.SendMessage != nil {
			configCount++
		}
		if a.AIPrompt != nil {
			configCount++
		}
		if configCount == 0 {
			return fmt.Errorf("action %d: exactly one action config must be set", i)
		}
		if configCount > 1 {
			return fmt.Errorf("action %d: exactly one action config must be set, got %d", i, configCount)
		}
		if a.AIPrompt != nil {
			// Reject unknown provider_type values up front so the
			// agent-only / service-only branches below don't silently
			// accept typos or future additions that haven't been wired
			// through the rest of the stack yet.
			if a.AIPrompt.ProviderType != AIProviderTypeAgent && a.AIPrompt.ProviderType != AIProviderTypeService {
				return fmt.Errorf("action %d: ai_prompt provider_type must be %q or %q", i, AIProviderTypeAgent, AIProviderTypeService)
			}
			// Tool support is agent-only at the bridge layer (services reject
			// allowed_tools with HTTP 400). Catch the misconfiguration at save
			// time so the user sees a clear error instead of an opaque
			// execute-time failure. Guardrails imply allowed_tools, so they
			// are also agent-only.
			if a.AIPrompt.ProviderType == AIProviderTypeService {
				if len(a.AIPrompt.AllowedTools) > 0 {
					return fmt.Errorf("action %d: allowed_tools is only supported with provider_type %q", i, AIProviderTypeAgent)
				}
				if a.AIPrompt.Guardrails != nil {
					return fmt.Errorf("action %d: guardrails is only supported with provider_type %q", i, AIProviderTypeAgent)
				}
			}
			if a.AIPrompt.Guardrails != nil {
				if len(a.AIPrompt.AllowedTools) == 0 {
					return fmt.Errorf("action %d: guardrails requires non-empty allowed_tools", i)
				}
				seenCh := make(map[string]struct{})
				for _, c := range a.AIPrompt.Guardrails.Channels {
					if !mmmodel.IsValidId(c.ChannelID) {
						return fmt.Errorf("action %d: invalid channel id %q in guardrails.channel_ids (expected 26-character Mattermost ID)", i, c.ChannelID)
					}
					if _, dup := seenCh[c.ChannelID]; dup {
						return fmt.Errorf("action %d: duplicate channel id %q in guardrails.channel_ids", i, c.ChannelID)
					}
					seenCh[c.ChannelID] = struct{}{}
				}
			}
			switch a.AIPrompt.RequestAs {
			case "", AIPromptRequestAsTriggerer, AIPromptRequestAsCreator:
			default:
				return fmt.Errorf("action %d: request_as must be %q or %q", i, AIPromptRequestAsTriggerer, AIPromptRequestAsCreator)
			}
		}
	}
	return nil
}
