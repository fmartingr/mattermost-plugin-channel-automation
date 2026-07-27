// Package hooks implements HTTP callbacks for MCP tool before hooks when
// ai_prompt actions use channel guardrails.
package hooks

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/gorilla/mux"
	"github.com/mattermost/mattermost-plugin-agents/public/mcptool"
	mmmodel "github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"

	"github.com/mattermost/mattermost-plugin-channel-automation/server/model"
)

const (
	maxHookBodySize = 1 << 20 // 1 MB

	// headerMattermostUserID is the request header Mattermost sets to the
	// authenticated session user ID for plugin HTTP requests.
	headerMattermostUserID = "Mattermost-User-ID"
)

// TriggerLookup is the narrow subset of automation.Registry that hook
// authorization needs: resolving a TriggerHandler from a trigger type. Kept
// as an interface to avoid an import cycle with the automation package and
// to make NewAPIHandler easy to fake in tests.
type TriggerLookup interface {
	GetTrigger(triggerType string) (model.TriggerHandler, bool)
}

// APIHandler serves tool hook callbacks for channel guardrails.
type APIHandler struct {
	store    model.Store
	api      plugin.API
	triggers TriggerLookup
	// channelTeamCache memoizes channel_id -> team_id (or "" for DM/GM).
	// Channel -> team is immutable in Mattermost so entries never expire.
	// On plugin restart the cache rebuilds on demand.
	channelTeamCache sync.Map
}

// HookCtx carries guardrail state and dependencies for a single hook invocation.
type HookCtx struct {
	Guardrails *model.Guardrails
	AllowedCh  map[string]struct{}
	// AllowedTeams is the set of team IDs the LLM is allowed to query through
	// team tools. It contains the team of every guardrail channel plus the
	// automation's trigger team (when resolvable).
	AllowedTeams map[string]struct{}
	API          plugin.API
}

// NewAPIHandler constructs a hooks API handler. triggers is used by hook
// authorization to delegate per-trigger-type "could this caller fire this
// trigger?" checks to the registered TriggerHandler instead of switching on
// trigger type here.
func NewAPIHandler(store model.Store, api plugin.API, triggers TriggerLookup) *APIHandler {
	return &APIHandler{store: store, api: api, triggers: triggers}
}

// HookURL returns the plugin-relative before-callback URL for a tool hook on
// the given automation/action. Centralized so the route registration and the URL
// emitted to the bridge cannot drift apart.
func HookURL(automationID, actionID string) string {
	return fmt.Sprintf("/api/v1/hooks/tools/%s/%s/before", automationID, actionID)
}

// RegisterRoutes registers POST /hooks/tools/{automation_id}/{action_id}/before.
func (h *APIHandler) RegisterRoutes(r *mux.Router) {
	r.HandleFunc("/hooks/tools/{automation_id}/{action_id}/before", h.handleBefore).Methods(http.MethodPost)
}

// resolveChannelTeam returns the team ID for the given channel, consulting
// the cache first and falling back to plugin.API.GetChannel. The result is
// memoized including the empty-team case (DM/GM channels).
func (h *APIHandler) resolveChannelTeam(channelID string) (string, error) {
	if channelID == "" {
		return "", nil
	}
	if v, ok := h.channelTeamCache.Load(channelID); ok {
		return v.(string), nil
	}
	ch, appErr := h.api.GetChannel(channelID)
	if appErr != nil {
		return "", fmt.Errorf("resolve channel %q team: %s", channelID, appErr.Error())
	}
	if ch == nil {
		return "", fmt.Errorf("resolve channel %q team: channel not found", channelID)
	}
	h.channelTeamCache.Store(channelID, ch.TeamId)
	return ch.TeamId, nil
}

// allowedSetsForGuardrails computes the AllowedCh and AllowedTeams sets for a
// guardrail block, resolving channel -> team for any entry not already
// carrying a TeamID. Channels whose team cannot be resolved are still added
// to AllowedCh (so channel tools work) but contribute nothing to
// AllowedTeams. The resolution is logged at debug for traceability.
func (h *APIHandler) allowedSetsForGuardrails(gr *model.Guardrails, automationID, actionID string) (map[string]struct{}, map[string]struct{}) {
	chs := make(map[string]struct{}, len(gr.Channels))
	teams := make(map[string]struct{}, len(gr.Channels))
	for i := range gr.Channels {
		c := &gr.Channels[i]
		cid := strings.TrimSpace(c.ChannelID)
		if cid == "" {
			continue
		}
		chs[cid] = struct{}{}
		if c.TeamID == "" {
			tid, err := h.resolveChannelTeam(cid)
			if err != nil {
				h.api.LogDebug("hooks: failed to resolve guardrail channel team",
					"automation_id", automationID,
					"action_id", actionID,
					"channel_id", cid,
					"error", err.Error(),
				)
				continue
			}
			c.TeamID = tid
		}
		if c.TeamID != "" {
			teams[c.TeamID] = struct{}{}
		}
	}
	return chs, teams
}

// authorizeHookCaller reports whether callerID may invoke a hook for the
// given automation/action. The automation creator may always invoke. When
// request_as is "creator", non-creators (including system admins) are
// rejected. Otherwise the caller must be a system admin or pass the
// trigger handler's CallerCanTrigger check, which mirrors the runtime
// dispatch filters.
func (h *APIHandler) authorizeHookCaller(callerID string, f *model.Automation, requestAs string) bool {
	if callerID == "" || f.CreatedBy == "" {
		return false
	}
	if callerID == f.CreatedBy {
		return true
	}
	if requestAs == model.AIPromptRequestAsCreator {
		return false
	}
	return h.callerCanTriggerAutomation(callerID, f)
}

// callerCanTriggerAutomation reports whether callerID could fire the
// automation. System admins always pass. Otherwise the per-trigger-type
// check is delegated to the registered TriggerHandler (which mirrors the
// same membership/user_type filters applied at dispatch time). An unknown
// trigger type or a missing handler returns false.
func (h *APIHandler) callerCanTriggerAutomation(callerID string, f *model.Automation) bool {
	if h.api.HasPermissionTo(callerID, mmmodel.PermissionManageSystem) {
		return true
	}
	if h.triggers == nil {
		return false
	}
	handler, ok := h.triggers.GetTrigger(f.Trigger.Type())
	if !ok {
		return false
	}
	return handler.CallerCanTrigger(h.api, &f.Trigger, callerID)
}

// loadGuardrailAutomation returns the automation and its ai_prompt config for
// the given action when channel guardrails are configured.
func (h *APIHandler) loadGuardrailAutomation(automationID, actionID string) (*model.Automation, *model.AIPromptActionConfig, bool) {
	f, err := h.store.Get(automationID)
	if err != nil || f == nil {
		return nil, nil, false
	}
	for i := range f.Actions {
		act := &f.Actions[i]
		if act.ID != actionID || act.AIPrompt == nil || act.AIPrompt.Guardrails == nil {
			continue
		}
		if len(act.AIPrompt.Guardrails.Channels) == 0 {
			continue
		}
		return f, act.AIPrompt, true
	}
	return nil, nil, false
}

// actionAllowsTool reports whether toolName is in allowed_tools, comparing both
// sides by their bare catalog name so bare and namespaced forms match while
// external tools sharing a bare name do not.
func actionAllowsTool(allowedTools []string, toolName string) bool {
	want := BareMattermostMCPToolName(toolName)
	for _, t := range allowedTools {
		if BareMattermostMCPToolName(t) == want {
			return true
		}
	}
	return false
}

// automationAnchorTeamID returns the Mattermost team ID the automation is anchored to:
// the team_id of a channel_created or user_joined_team trigger, or the team
// of the trigger channel for channel-scoped triggers. It is best-effort:
// benign "no anchor team" cases (no trigger channel, channel_created/user_joined_team
// without team_id, DM/GM trigger channel) return ("", nil). Only unexpected
// failures (nil automation, GetChannel error) are returned as errors so callers can
// log them.
func automationAnchorTeamID(api plugin.API, f *model.Automation) (string, error) {
	if f == nil {
		return "", fmt.Errorf("automation not loaded")
	}
	if f.Trigger.ChannelCreated != nil {
		return f.Trigger.ChannelCreated.TeamID, nil
	}
	if f.Trigger.UserJoinedTeam != nil {
		return f.Trigger.UserJoinedTeam.TeamID, nil
	}
	chID := f.TriggerChannelID()
	if chID == "" {
		return "", nil
	}
	ch, appErr := api.GetChannel(chID)
	if appErr != nil {
		return "", fmt.Errorf("get channel %q: %w", chID, appErr)
	}
	if ch == nil {
		return "", nil
	}
	return ch.TeamId, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func readJSONBody(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, maxHookBodySize)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("trailing data in JSON body")
	}
	return nil
}

func (h *APIHandler) handleBefore(w http.ResponseWriter, r *http.Request) {
	var req mcptool.BeforeHookRequest
	if err := readJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, mcptool.BeforeHookResponse{Error: "invalid request body"})
		return
	}
	vars := mux.Vars(r)
	automationID := vars["automation_id"]
	actionID := vars["action_id"]

	f, ai, ok := h.loadGuardrailAutomation(automationID, actionID)
	if !ok {
		writeJSON(w, http.StatusOK, mcptool.BeforeHookResponse{Error: "guardrails not found"})
		return
	}

	callerID := r.Header.Get(headerMattermostUserID)
	if !h.authorizeHookCaller(callerID, f, ai.RequestAs) {
		h.api.LogWarn("hooks: unauthorized hook caller",
			"automation_id", f.ID,
			"caller_id", callerID,
		)
		writeJSON(w, http.StatusForbidden, mcptool.BeforeHookResponse{
			Error: "forbidden: the current user is not authorized to invoke this automation's tool hook",
		})
		return
	}

	if req.ToolName == "" {
		writeJSON(w, http.StatusOK, mcptool.BeforeHookResponse{Error: "tool_name is required"})
		return
	}

	if !actionAllowsTool(ai.AllowedTools, req.ToolName) {
		h.api.LogWarn("hooks: tool not in action allowed_tools",
			"automation_id", f.ID,
			"action_id", actionID,
			"tool_name", req.ToolName,
		)
		writeJSON(w, http.StatusOK, mcptool.BeforeHookResponse{
			Error: fmt.Sprintf("tool %q is not in this automation action's allowed_tools", req.ToolName),
		})
		return
	}

	entry, catOK := LookupMattermostMCPTool(req.ToolName)
	if !catOK {
		writeJSON(w, http.StatusOK, mcptool.BeforeHookResponse{
			Error: fmt.Sprintf("tool %q is not a known Mattermost MCP server tool; channel guardrails reject unrecognized tools",
				req.ToolName),
		})
		return
	}

	if !entry.Allowed {
		writeJSON(w, http.StatusOK, mcptool.BeforeHookResponse{
			Error: fmt.Sprintf("tool %q is not permitted in automations", req.ToolName),
		})
		return
	}
	if entry.Before == nil {
		writeJSON(w, http.StatusOK, mcptool.BeforeHookResponse{
			Error: fmt.Sprintf("tool %q is not supported with channel guardrails (Mattermost MCP tools must be explicitly allowlisted for guardrails)",
				req.ToolName),
		})
		return
	}

	var argsMap map[string]any
	if len(req.Args) > 0 {
		if err := json.Unmarshal(req.Args, &argsMap); err != nil {
			writeJSON(w, http.StatusOK, mcptool.BeforeHookResponse{Error: "invalid args JSON in hook request"})
			return
		}
	}

	allowedCh, allowedTeams := h.allowedSetsForGuardrails(ai.Guardrails, automationID, actionID)
	if expTeam, err := automationAnchorTeamID(h.api, f); err != nil {
		h.api.LogDebug("hooks: failed to resolve automation anchor team",
			"automation_id", automationID, "action_id", actionID, "error", err.Error())
	} else if expTeam != "" {
		allowedTeams[expTeam] = struct{}{}
	}

	ctx := HookCtx{
		Guardrails:   ai.Guardrails,
		AllowedCh:    allowedCh,
		AllowedTeams: allowedTeams,
		API:          h.api,
	}
	if err := entry.Before(ctx, argsMap); err != nil {
		writeJSON(w, http.StatusOK, mcptool.BeforeHookResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, mcptool.BeforeHookResponse{})
}

func stringArg(args map[string]any, key string) string {
	s, _ := args[key].(string)
	return strings.TrimSpace(s)
}
