package automation

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/gorilla/mux"
	mmmodel "github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"

	"github.com/mattermost/mattermost-plugin-channel-automation/server/automation/hooks"
	"github.com/mattermost/mattermost-plugin-channel-automation/server/httputil"
	"github.com/mattermost/mattermost-plugin-channel-automation/server/model"
	"github.com/mattermost/mattermost-plugin-channel-automation/server/permissions"
)

const maxRequestBodySize = 1 << 20 // 1 MB

// APIHandler provides HTTP handlers for automation CRUD operations.
type APIHandler struct {
	store           model.Store
	historyStore    model.ExecutionStore
	api             plugin.API
	registry        *Registry
	scheduleManager *ScheduleManager
	config          model.Configuration
	bridge          hooks.AgentToolsLister
}

// NewAPIHandler creates a new automation API handler. bridge may be nil in tests
// that do not exercise allowed_tools validation.
func NewAPIHandler(store model.Store, historyStore model.ExecutionStore, api plugin.API, registry *Registry, scheduleManager *ScheduleManager, config model.Configuration, bridge hooks.AgentToolsLister) *APIHandler {
	return &APIHandler{store: store, historyStore: historyStore, api: api, registry: registry, scheduleManager: scheduleManager, config: config, bridge: bridge}
}

// RegisterRoutes registers the automation CRUD routes on the given router.
func (h *APIHandler) RegisterRoutes(r *mux.Router) {
	r.HandleFunc("/automations", h.handleListAutomations).Methods(http.MethodGet)
	r.HandleFunc("/automations", h.handleCreateAutomation).Methods(http.MethodPost)
	r.HandleFunc("/automations/{id}", h.handleGetAutomation).Methods(http.MethodGet)
	r.HandleFunc("/automations/{id}", h.handleUpdateAutomation).Methods(http.MethodPut, http.MethodPatch)
	r.HandleFunc("/automations/{id}", h.handleDeleteAutomation).Methods(http.MethodDelete)
}

// channelAutomationLimit returns the configured per-channel automation
// limit, or 0 when no limit is configured.
func (h *APIHandler) channelAutomationLimit() int {
	if h.config == nil {
		return 0
	}
	return h.config.MaxAutomationsPerChannel()
}

func (h *APIHandler) handleListAutomations(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("Mattermost-User-ID")
	if userID == "" {
		httputil.WriteErrorJSON(w, http.StatusUnauthorized, "missing Mattermost-User-ID header", "")
		return
	}

	var automations []*model.Automation
	var err error
	if channelID := r.URL.Query().Get("channel_id"); channelID != "" {
		automations, err = h.store.ListByTriggerChannel(channelID)
	} else {
		automations, err = h.store.List()
	}
	if err != nil {
		h.api.LogError("Failed to list automations", "user_id", userID, "error", err.Error())
		httputil.WriteErrorJSON(w, http.StatusInternalServerError, "failed to list automations", err.Error())
		return
	}

	// Filter automations to only those the user has permission to view.
	// We deliberately distinguish backend failures from genuine permission
	// denials here: a permission-verification 5xx must not be silently
	// swallowed as "user can't see this automation", because that would
	// produce a misleading partial 200 OK while the permission backend is
	// broken.
	isAdmin := h.api.HasPermissionTo(userID, mmmodel.PermissionManageSystem)
	if !isAdmin {
		visible := make([]*model.Automation, 0, len(automations))
		for _, a := range automations {
			permErr := permissions.CanViewAutomation(h.api, userID, a)
			if permErr == nil {
				visible = append(visible, a)
				continue
			}
			var appErr *mmmodel.AppError
			if errors.As(permErr, &appErr) {
				h.api.LogError("Failed to verify permissions while listing automations",
					"user_id", userID, "automation_id", a.ID, "error", permErr.Error())
				httputil.WriteErrorJSON(w, http.StatusInternalServerError, "failed to verify permissions", "")
				return
			}
			// Genuine permission denial — skip this automation.
		}
		automations = visible
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(automations); err != nil {
		h.api.LogError("Failed to encode automations", "error", err.Error())
	}
}

func (h *APIHandler) handleCreateAutomation(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("Mattermost-User-ID")
	if userID == "" {
		httputil.WriteErrorJSON(w, http.StatusUnauthorized, "missing Mattermost-User-ID header", "")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
	var f model.Automation
	if err := json.NewDecoder(r.Body).Decode(&f); err != nil {
		httputil.WriteErrorJSON(w, http.StatusBadRequest, "invalid request body", "")
		return
	}

	f.ID = mmmodel.NewId()
	f.CreatedAt = model.NowTimestamp()
	f.UpdatedAt = f.CreatedAt
	f.CreatedBy = userID

	if f.Name == "" {
		httputil.WriteErrorJSON(w, http.StatusBadRequest, "name is required", "")
		return
	}
	if len(f.Name) > 100 {
		httputil.WriteErrorJSON(w, http.StatusBadRequest, "name must be 100 characters or fewer", "")
		return
	}

	if err := ValidateTrigger(h.registry, &f.Trigger, nil); err != nil {
		httputil.WriteErrorJSON(w, http.StatusBadRequest, err.Error(), "")
		return
	}

	if err := model.ValidateActions(f.Actions); err != nil {
		httputil.WriteErrorJSON(w, http.StatusBadRequest, err.Error(), "")
		return
	}

	if err := model.ValidateSendMessageChannel(&f); err != nil {
		httputil.WriteErrorJSON(w, http.StatusBadRequest, err.Error(), "")
		return
	}

	// Creator-locked triggers require request_as=creator; reject
	// triggerer/default so the restriction is explicit rather than silent.
	if err := model.ValidateCreatorLockedRequestAs(&f); err != nil {
		httputil.WriteErrorJSON(w, http.StatusBadRequest, err.Error(), "")
		return
	}

	if err := rejectBotAIPromptCreator(h.api, f.CreatedBy, &f); err != nil {
		httputil.WriteErrorJSON(w, http.StatusBadRequest, err.Error(), "")
		return
	}

	if err := permissions.CheckAutomationPermissions(h.api, f.CreatedBy, &f); err != nil {
		msg, code, detail := permissions.HandlePermissionError(h.api, err, f.CreatedBy, f.ID)
		httputil.WriteErrorJSON(w, code, msg, detail)
		return
	}

	if err := permissions.CheckGuardrailChannelPermissions(h.api, f.CreatedBy, &f); err != nil {
		msg, code, detail := permissions.HandlePermissionError(h.api, err, f.CreatedBy, f.ID)
		httputil.WriteErrorJSON(w, code, msg, detail)
		return
	}

	// Bridge-backed agent access verification runs after the local permission
	// checks so we never call the bridge for automations the user cannot manage.
	if err := hooks.ValidateAllowedTools(&f, f.CreatedBy, h.bridge); err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, hooks.ErrToolDiscovery) {
			code = http.StatusBadGateway
		}
		httputil.WriteErrorJSON(w, code, err.Error(), "")
		return
	}

	// Runs after ValidateAllowedTools so a blocked or unavailable tool is
	// reported as such rather than as a request_as problem.
	if err := hooks.ValidateCreatorOnlyToolsForTriggerer(&f); err != nil {
		httputil.WriteErrorJSON(w, http.StatusBadRequest, err.Error(), "")
		return
	}

	// No triggering user exists at configuration time, so the exempt-triggerer
	// dimension does not apply here.
	if err := permissions.CheckGuardrailsRequired(h.api, &f, ""); err != nil {
		msg, code, detail := permissions.HandlePermissionError(h.api, err, f.CreatedBy, f.ID)
		httputil.WriteErrorJSON(w, code, msg, detail)
		return
	}

	limit := h.channelAutomationLimit()
	if err := h.store.SaveWithChannelLimit(&f, limit, ""); err != nil {
		if errors.Is(err, model.ErrChannelAutomationLimitExceeded) {
			msg := fmt.Sprintf("channel has reached the maximum of %d automation(s)", limit)
			httputil.WriteErrorJSON(w, http.StatusConflict, msg, "")
			return
		}
		h.api.LogError("Failed to create automation", "user_id", f.CreatedBy, "automation_id", f.ID, "error", err.Error())
		httputil.WriteErrorJSON(w, http.StatusInternalServerError, "failed to create automation", err.Error())
		return
	}

	if h.scheduleManager != nil {
		if err := h.scheduleManager.SyncAutomation(nil, &f); err != nil {
			h.api.LogWarn("Failed to sync schedule after automation create", "automation_id", f.ID, "error", err.Error())
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(&f); err != nil {
		h.api.LogError("Failed to encode automation", "error", err.Error())
	}
}

func (h *APIHandler) handleGetAutomation(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("Mattermost-User-ID")
	if userID == "" {
		httputil.WriteErrorJSON(w, http.StatusUnauthorized, "missing Mattermost-User-ID header", "")
		return
	}

	id := mux.Vars(r)["id"]

	f, err := h.store.Get(id)
	if err != nil {
		h.api.LogError("Failed to get automation", "automation_id", id, "error", err.Error())
		httputil.WriteErrorJSON(w, http.StatusInternalServerError, "failed to get automation", err.Error())
		return
	}
	if f == nil {
		httputil.WriteErrorJSON(w, http.StatusNotFound, "automation not found", "")
		return
	}

	if err := permissions.CanViewAutomation(h.api, userID, f); err != nil {
		msg, code, detail := permissions.HandlePermissionError(h.api, err, userID, id)
		httputil.WriteErrorJSON(w, code, msg, detail)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(f); err != nil {
		h.api.LogError("Failed to encode automation", "error", err.Error())
	}
}

func (h *APIHandler) handleUpdateAutomation(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("Mattermost-User-ID")
	if userID == "" {
		httputil.WriteErrorJSON(w, http.StatusUnauthorized, "missing Mattermost-User-ID header", "")
		return
	}

	id := mux.Vars(r)["id"]

	existing, err := h.store.Get(id)
	if err != nil {
		h.api.LogError("Failed to get automation for update", "automation_id", id, "error", err.Error())
		httputil.WriteErrorJSON(w, http.StatusInternalServerError, "failed to get automation", err.Error())
		return
	}
	if existing == nil {
		httputil.WriteErrorJSON(w, http.StatusNotFound, "automation not found", "")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
	var update model.AutomationUpdate
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		httputil.WriteErrorJSON(w, http.StatusBadRequest, "invalid request body", "")
		return
	}

	// Copy the existing record and overlay mutable fields from the request.
	// Update preserves Enabled when the payload omits it (a partial PUT
	// must not silently disable a running automation), and leaves the
	// immutable fields (ID, CreatedAt, CreatedBy) untouched.
	f := *existing
	f.Update(&update)
	f.UpdatedAt = model.NowTimestamp()

	// Only the creator (or a sysadmin acting on their behalf) may edit an automation.
	// This is the security boundary that lets the downstream creator-anchored
	// checks below validate against existing.CreatedBy without enabling
	// privilege escalation by a non-creator editor.
	if err := permissions.CanEditAutomation(h.api, userID, existing); err != nil {
		msg, code, detail := permissions.HandlePermissionError(h.api, err, userID, id)
		httputil.WriteErrorJSON(w, code, msg, detail)
		return
	}

	if f.Name == "" {
		httputil.WriteErrorJSON(w, http.StatusBadRequest, "name is required", "")
		return
	}
	if len(f.Name) > 100 {
		httputil.WriteErrorJSON(w, http.StatusBadRequest, "name must be 100 characters or fewer", "")
		return
	}

	if err := model.ValidateActions(f.Actions); err != nil {
		httputil.WriteErrorJSON(w, http.StatusBadRequest, err.Error(), "")
		return
	}

	if err := model.ValidateSendMessageChannel(&f); err != nil {
		httputil.WriteErrorJSON(w, http.StatusBadRequest, err.Error(), "")
		return
	}

	// Creator-locked triggers require request_as=creator; reject
	// triggerer/default so the restriction is explicit rather than silent.
	if err := model.ValidateCreatorLockedRequestAs(&f); err != nil {
		httputil.WriteErrorJSON(w, http.StatusBadRequest, err.Error(), "")
		return
	}

	if err := rejectBotAIPromptCreator(h.api, f.CreatedBy, &f); err != nil {
		httputil.WriteErrorJSON(w, http.StatusBadRequest, err.Error(), "")
		return
	}

	if err := ValidateTrigger(h.registry, &f.Trigger, &existing.Trigger); err != nil {
		httputil.WriteErrorJSON(w, http.StatusBadRequest, err.Error(), "")
		return
	}

	// The editor must be allowed to manage the new configuration (membership
	// and role requirements apply to the acting user).
	if err := permissions.CheckAutomationPermissions(h.api, userID, &f); err != nil {
		msg, code, detail := permissions.HandlePermissionError(h.api, err, userID, id)
		httputil.WriteErrorJSON(w, code, msg, detail)
		return
	}

	// When a sysadmin edits on behalf of the creator, the configuration must
	// also remain admissible for the creator at runtime.
	if userID != existing.CreatedBy {
		if err := permissions.CheckAutomationPermissions(h.api, existing.CreatedBy, &f); err != nil {
			msg, code, detail := permissions.HandlePermissionError(h.api, err, existing.CreatedBy, id)
			httputil.WriteErrorJSON(w, code, msg, detail)
			return
		}
	}

	if err := permissions.CheckGuardrailChannelPermissions(h.api, existing.CreatedBy, &f); err != nil {
		msg, code, detail := permissions.HandlePermissionError(h.api, err, existing.CreatedBy, id)
		httputil.WriteErrorJSON(w, code, msg, detail)
		return
	}

	// Bridge-backed agent access verification uses the original creator's
	// identity (matches the runtime model where the bridge ACL is checked
	// against created_by, not the editor) and runs after the local permission
	// checks so we never call the bridge for inadmissible automations.
	if err := hooks.ValidateAllowedTools(&f, f.CreatedBy, h.bridge); err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, hooks.ErrToolDiscovery) {
			code = http.StatusBadGateway
		}
		httputil.WriteErrorJSON(w, code, err.Error(), "")
		return
	}

	// Runs after ValidateAllowedTools so a blocked or unavailable tool is
	// reported as such rather than as a request_as problem.
	if err := hooks.ValidateCreatorOnlyToolsForTriggerer(&f); err != nil {
		httputil.WriteErrorJSON(w, http.StatusBadRequest, err.Error(), "")
		return
	}

	// No triggering user exists at configuration time, so the exempt-triggerer
	// dimension does not apply here.
	if err := permissions.CheckGuardrailsRequired(h.api, &f, ""); err != nil {
		msg, code, detail := permissions.HandlePermissionError(h.api, err, existing.CreatedBy, id)
		httputil.WriteErrorJSON(w, code, msg, detail)
		return
	}

	limit := h.channelAutomationLimit()
	if err := h.store.SaveWithChannelLimit(&f, limit, f.ID); err != nil {
		if errors.Is(err, model.ErrChannelAutomationLimitExceeded) {
			msg := fmt.Sprintf("channel has reached the maximum of %d automation(s)", limit)
			httputil.WriteErrorJSON(w, http.StatusConflict, msg, "")
			return
		}
		h.api.LogError("Failed to update automation", "user_id", userID, "automation_id", id, "error", err.Error())
		httputil.WriteErrorJSON(w, http.StatusInternalServerError, "failed to update automation", err.Error())
		return
	}

	if h.scheduleManager != nil {
		if err := h.scheduleManager.SyncAutomation(existing, &f); err != nil {
			h.api.LogWarn("Failed to sync schedule after automation update", "automation_id", f.ID, "error", err.Error())
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(&f); err != nil {
		h.api.LogError("Failed to encode automation", "error", err.Error())
	}
}

func (h *APIHandler) handleDeleteAutomation(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("Mattermost-User-ID")
	if userID == "" {
		httputil.WriteErrorJSON(w, http.StatusUnauthorized, "missing Mattermost-User-ID header", "")
		return
	}

	id := mux.Vars(r)["id"]

	existing, err := h.store.Get(id)
	if err != nil {
		h.api.LogError("Failed to get automation for delete", "automation_id", id, "error", err.Error())
		httputil.WriteErrorJSON(w, http.StatusInternalServerError, "failed to get automation", err.Error())
		return
	}
	if existing == nil {
		httputil.WriteErrorJSON(w, http.StatusNotFound, "automation not found", "")
		return
	}

	if err := permissions.CanEditAutomation(h.api, userID, existing); err != nil {
		msg, code, detail := permissions.HandlePermissionError(h.api, err, userID, id)
		httputil.WriteErrorJSON(w, code, msg, detail)
		return
	}

	if err := h.store.Delete(id); err != nil {
		h.api.LogError("Failed to delete automation", "user_id", userID, "automation_id", id, "error", err.Error())
		httputil.WriteErrorJSON(w, http.StatusInternalServerError, "failed to delete automation", err.Error())
		return
	}

	if h.scheduleManager != nil {
		h.scheduleManager.RemoveAutomation(id)
	}

	if h.historyStore != nil {
		if err := h.historyStore.PurgeAutomation(id); err != nil {
			h.api.LogError("Failed to purge execution history", "user_id", userID, "automation_id", id, "error", err.Error())
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// rejectBotAIPromptCreator rejects automations that contain an ai_prompt action
// when the automation creator is a bot account. Bot accounts must not own AI
// automations so the resolved ai_prompt user can never be a bot via the creator
// path. Transient GetUser failures surface as errors so we fail closed.
func rejectBotAIPromptCreator(api plugin.API, creatorID string, f *model.Automation) error {
	if f == nil || creatorID == "" {
		return nil
	}
	hasAI := false
	for i := range f.Actions {
		if f.Actions[i].AIPrompt != nil {
			hasAI = true
			break
		}
	}
	if !hasAI {
		return nil
	}
	user, appErr := api.GetUser(creatorID)
	if appErr != nil {
		return fmt.Errorf("failed to verify automation creator: %s", appErr.Error())
	}
	if user != nil && user.IsBot {
		return fmt.Errorf("bot accounts cannot create or own automations with ai_prompt actions")
	}
	return nil
}
