package permissions

import (
	"errors"
	"fmt"
	"net/http"

	mmmodel "github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"

	"github.com/mattermost/mattermost-plugin-channel-automation/server/model"
)

// CheckAutomationPermissions verifies that userID has permission to manage the automation.
// For channel_created automations the user must be a team admin on the trigger's team
// (system admins bypass), and all literal channel references must belong to that team.
// For user_joined_team automations the user must be a team admin on the referenced teams
// (system admins bypass). For other automations every user, including system admins, must
// be a member of each literal channel referenced in the automation; non-admins must also
// hold PermissionManageChannelRoles on public/private channels (which covers channel
// admins and team admins via scheme resolution).
//
// When no concrete channels can be verified (e.g. only templated or AI-only
// actions on a non-channel_created trigger), we require system admin permission.
func CheckAutomationPermissions(api plugin.API, userID string, f *model.Automation) error {
	isAdmin := api.HasPermissionTo(userID, mmmodel.PermissionManageSystem)

	// channel_created and user_joined_team automations use team-level authorization.
	// We call GetTeam first because HasPermissionToTeam is boolean-only and
	// cannot distinguish infrastructure failures from genuine permission denials.
	if f.Trigger.ChannelCreated != nil {
		if isAdmin {
			return nil
		}
		teamID := f.Trigger.ChannelCreated.TeamID
		if _, appErr := api.GetTeam(teamID); appErr != nil {
			if appErr.StatusCode >= http.StatusInternalServerError {
				return fmt.Errorf("failed to verify team: %w", appErr)
			}
			return fmt.Errorf("team %s not found or not accessible", teamID)
		}
		if !api.HasPermissionToTeam(userID, teamID, mmmodel.PermissionManageTeam) {
			return fmt.Errorf("you must be a team admin on the team specified in the channel_created trigger")
		}
		for _, chID := range model.CollectChannelIDs(f) {
			ch, appErr := api.GetChannel(chID)
			if appErr != nil {
				if appErr.StatusCode >= http.StatusInternalServerError {
					return fmt.Errorf("failed to verify channel team membership: %w", appErr)
				}
				return fmt.Errorf("channel %s not found or not accessible", chID)
			}
			if ch.TeamId != teamID {
				return fmt.Errorf("channel %s does not belong to the team specified in the channel_created trigger", chID)
			}
		}
		return nil
	}

	if f.Trigger.UserJoinedTeam != nil {
		if isAdmin {
			return nil
		}
		teamIDs := model.CollectTeamIDs(f)
		if len(teamIDs) == 0 {
			return fmt.Errorf("system admin permission is required for automations without explicit team references")
		}
		for _, teamID := range teamIDs {
			if _, appErr := api.GetTeam(teamID); appErr != nil {
				if appErr.StatusCode >= http.StatusInternalServerError {
					return fmt.Errorf("failed to verify team: %w", appErr)
				}
				return fmt.Errorf("team %s not found or not accessible", teamID)
			}
			if !api.HasPermissionToTeam(userID, teamID, mmmodel.PermissionManageTeam) {
				return fmt.Errorf("you must be a team admin on all teams referenced by this automation")
			}
		}
		return nil
	}

	channelIDs := model.CollectChannelIDs(f)
	if len(channelIDs) == 0 {
		if isAdmin {
			return nil
		}
		return fmt.Errorf("system admin permission is required for automations without explicit channel references")
	}
	for _, chID := range channelIDs {
		ch, chErr := api.GetChannel(chID)
		if chErr != nil {
			if chErr.StatusCode >= http.StatusInternalServerError {
				return fmt.Errorf("failed to verify channel permissions: %w", chErr)
			}
			return fmt.Errorf("you do not have permission to manage one or more channels referenced by this automation")
		}
		if ch == nil {
			return fmt.Errorf("you do not have permission to manage one or more channels referenced by this automation")
		}
		// Membership is required for everyone (including sysadmins): a user
		// should not manage automations in a channel they have not joined.
		if _, appErr := api.GetChannelMember(chID, userID); appErr != nil {
			if appErr.StatusCode >= http.StatusInternalServerError {
				return fmt.Errorf("failed to verify channel permissions: %w", appErr)
			}
			return fmt.Errorf("you do not have permission to manage one or more channels referenced by this automation")
		}
		// DM and GM channels have no channel-admin role: membership alone suffices.
		if ch.Type == mmmodel.ChannelTypeDirect || ch.Type == mmmodel.ChannelTypeGroup {
			continue
		}
		if !isAdmin && !api.HasPermissionToChannel(userID, chID, mmmodel.PermissionManageChannelRoles) {
			return fmt.Errorf("you do not have permission to manage one or more channels referenced by this automation")
		}
	}
	return nil
}

// CanViewAutomation reports whether userID may read an automation via GET or list.
// System admins may view any automation regardless of channel membership. Other
// users must satisfy CheckAutomationPermissions.
func CanViewAutomation(api plugin.API, userID string, f *model.Automation) error {
	if api.HasPermissionTo(userID, mmmodel.PermissionManageSystem) {
		return nil
	}
	return CheckAutomationPermissions(api, userID, f)
}

// CanEditAutomation returns nil when userID is permitted to modify or delete the
// given automation. Editors are restricted to the automation's creator or a system admin.
//
// Limiting edits to creator-or-sysadmin is the security boundary that lets
// downstream checks (CheckGuardrailChannelPermissions, ValidateAllowedTools)
// safely validate against the creator's permissions rather than the editor's:
// non-creator editors are sysadmins (already maximally privileged), and the
// agent always runs with the creator's identity at execute time, so there is
// no privilege-escalation path through editor-supplied configuration.
func CanEditAutomation(api plugin.API, userID string, f *model.Automation) error {
	if api.HasPermissionTo(userID, mmmodel.PermissionManageSystem) {
		return nil
	}
	if f != nil && f.CreatedBy != "" && userID == f.CreatedBy {
		return nil
	}
	return fmt.Errorf("only the automation creator or a system admin may modify this automation")
}

// CheckGuardrailChannelPermissions verifies that userID can read every channel
// referenced by ai_prompt guardrails on the automation. Callers should pass the
// automation's creator (CreatedBy): the AI agent runs with the creator's identity at
// execute time, so a guardrail channel the creator cannot read would silently
// break the automation. Authorization to edit the automation is enforced separately
// by CanEditAutomation.
//
// Access requires actual channel membership for every channel type — public,
// private, group, and direct — for everyone including system admins.
func CheckGuardrailChannelPermissions(api plugin.API, userID string, f *model.Automation) error {
	seen := make(map[string]struct{})
	for i := range f.Actions {
		ai := f.Actions[i].AIPrompt
		if ai == nil || ai.Guardrails == nil {
			continue
		}
		for _, c := range ai.Guardrails.Channels {
			if c.ChannelID == "" {
				continue
			}
			if _, dup := seen[c.ChannelID]; dup {
				continue
			}
			seen[c.ChannelID] = struct{}{}

			// GetChannel first so 5xx infrastructure failures surface
			// distinctly from genuine "no such channel" / "no access" cases,
			// matching the pattern used in the channel_created branch above.
			if _, appErr := api.GetChannel(c.ChannelID); appErr != nil {
				if appErr.StatusCode >= http.StatusInternalServerError {
					return fmt.Errorf("failed to verify guardrail channel: %w", appErr)
				}
				return fmt.Errorf("you do not have permission to read one or more channels referenced by ai_prompt guardrails")
			}

			if _, appErr := api.GetChannelMember(c.ChannelID, userID); appErr != nil {
				if appErr.StatusCode >= http.StatusInternalServerError {
					return fmt.Errorf("failed to verify guardrail channel membership: %w", appErr)
				}
				return fmt.Errorf("you do not have permission to read one or more channels referenced by ai_prompt guardrails")
			}
		}
	}
	return nil
}

// HandlePermissionError determines the appropriate HTTP status and log level
// based on whether the permission check failed due to an API/infrastructure
// error (500) or the user genuinely lacking permissions (403).
func HandlePermissionError(api plugin.API, err error, userID, contextID string) (string, int, string) {
	var appErr *mmmodel.AppError
	if errors.As(err, &appErr) {
		api.LogError("Failed to verify permissions", "user_id", userID, "context_id", contextID, "error", err.Error())
		return "failed to verify permissions", http.StatusInternalServerError, err.Error()
	}
	api.LogWarn("Permission denied", "user_id", userID, "context_id", contextID, "error", err.Error())
	return err.Error(), http.StatusForbidden, ""
}
