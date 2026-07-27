package automation

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/mattermost/mattermost-plugin-agents/public/bridgeclient"
	mmmodel "github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin/plugintest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/mattermost/mattermost-plugin-channel-automation/server/model"
)

// testConfig is a simple Configuration implementation for tests.
type testConfig struct {
	maxAutomationsPerChannel int
}

func (c *testConfig) MaxAutomationsPerChannel() int {
	return c.maxAutomationsPerChannel
}

// expectLogCalls registers permissive LogError and LogWarn expectations that
// accept any number of arguments. This covers enriched log calls that include
// user_id, automation_id, and other context fields.
func expectLogCalls(api *plugintest.API) {
	// LogError with 3, 5, 7, or 9 args (msg + 1-4 key-value pairs).
	api.On("LogError", mock.Anything, mock.Anything, mock.Anything).Maybe()
	api.On("LogError", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Maybe()
	api.On("LogError", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Maybe()
	api.On("LogError", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Maybe()
	// LogWarn with 3, 5, 7, or 9 args.
	api.On("LogWarn", mock.Anything, mock.Anything, mock.Anything).Maybe()
	api.On("LogWarn", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Maybe()
	api.On("LogWarn", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Maybe()
	api.On("LogWarn", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Maybe()
}

func setupAPI(t *testing.T) (*mux.Router, model.Store, *plugintest.API) {
	t.Helper()

	store, _ := setupStore(t)

	api := &plugintest.API{}
	expectLogCalls(api)
	api.On("HasPermissionTo", mock.Anything, mmmodel.PermissionManageSystem).Return(false).Maybe()
	api.On("GetChannel", mock.Anything).Return(
		&mmmodel.Channel{Type: mmmodel.ChannelTypeOpen}, nil,
	).Maybe()
	api.On("GetChannelMember", mock.Anything, mock.Anything).Return(&mmmodel.ChannelMember{}, nil).Maybe()
	api.On("HasPermissionToChannel", mock.Anything, mock.Anything, mmmodel.PermissionManageChannelRoles).Return(true).Maybe()

	handler := NewAPIHandler(store, nil, api, newTestRegistry(), nil, nil, nil)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	return router, store, api
}

func TestAPI_CreateAutomation(t *testing.T) {
	router, store, _ := setupAPI(t)

	body := `{
		"name": "Test Automation",
		"enabled": true,
		"trigger": {"message_posted": {"channel_id": "ch1"}},
		"actions": [{"id": "send-message", "send_message": {"channel_id": "ch1", "body": "hello"}}]
	}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/automations", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)

	require.Equal(t, http.StatusCreated, w.Code)

	var created model.Automation
	require.NoError(t, json.NewDecoder(w.Body).Decode(&created))
	assert.NotEmpty(t, created.ID)
	assert.Equal(t, "Test Automation", created.Name)
	assert.True(t, created.Enabled)
	assert.Equal(t, "user1", created.CreatedBy)
	assert.NotZero(t, created.CreatedAt)
	assert.Equal(t, created.CreatedAt, created.UpdatedAt)
	require.Len(t, created.Actions, 1)
	assert.Equal(t, "send-message", created.Actions[0].ID)
	require.NotNil(t, created.Actions[0].SendMessage)
	assert.Equal(t, "hello", created.Actions[0].SendMessage.Body)

	// Verify it was persisted.
	got, err := store.Get(created.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, created.Name, got.Name)
}

func TestAPI_CreateAutomation_InvalidBody(t *testing.T) {
	router, _, _ := setupAPI(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/automations", bytes.NewBufferString("not json"))
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestAPI_CreateAutomation_InvalidActionID(t *testing.T) {
	router, _, _ := setupAPI(t)

	body := `{
		"name": "Test Automation",
		"enabled": true,
		"trigger": {"message_posted": {"channel_id": "ch1"}},
		"actions": [{"id": "BAD ID", "send_message": {"channel_id": "ch2", "body": "hello"}}]
	}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/automations", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "invalid")
}

func TestAPI_CreateAutomation_MissingActionID(t *testing.T) {
	router, _, _ := setupAPI(t)

	body := `{
		"name": "Test Automation",
		"enabled": true,
		"trigger": {"message_posted": {"channel_id": "ch1"}},
		"actions": [{"send_message": {"channel_id": "ch2", "body": "hello"}}]
	}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/automations", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "id is required")
}

func TestAPI_CreateAutomation_DuplicateActionIDs(t *testing.T) {
	router, _, _ := setupAPI(t)

	body := `{
		"name": "Test Automation",
		"enabled": true,
		"trigger": {"message_posted": {"channel_id": "ch1"}},
		"actions": [
			{"id": "send-msg", "send_message": {"channel_id": "ch2", "body": "hello"}},
			{"id": "send-msg", "send_message": {"channel_id": "ch3", "body": "world"}}
		]
	}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/automations", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "duplicate")
}

func TestAPI_GetAutomation(t *testing.T) {
	router, store, _ := setupAPI(t)

	require.NoError(t, store.Save(&model.Automation{
		ID:      "f1",
		Name:    "Automation 1",
		Trigger: model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch1"}},
	}))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/automations/f1", nil)
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)

	require.Equal(t, http.StatusOK, w.Code)

	var got model.Automation
	require.NoError(t, json.NewDecoder(w.Body).Decode(&got))
	assert.Equal(t, "f1", got.ID)
	assert.Equal(t, "Automation 1", got.Name)
}

func TestAPI_GetAutomation_NotFound(t *testing.T) {
	router, _, _ := setupAPI(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/automations/nonexistent", nil)
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestAPI_GetAutomation_SystemAdminNotMemberAllowed(t *testing.T) {
	api := &plugintest.API{}
	expectLogCalls(api)
	api.On("HasPermissionTo", "admin1", mmmodel.PermissionManageSystem).Return(true)

	router, store := setupAPIWithCustomMock(t, api)

	require.NoError(t, store.Save(&model.Automation{
		ID:      "f1",
		Name:    "Automation 1",
		Trigger: model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch1"}},
	}))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/automations/f1", nil)
	r.Header.Set("Mattermost-User-ID", "admin1")

	router.ServeHTTP(w, r)

	require.Equal(t, http.StatusOK, w.Code)
	api.AssertNotCalled(t, "GetChannel", mock.Anything)
	api.AssertNotCalled(t, "GetChannelMember", mock.Anything, mock.Anything)
}

func TestAPI_ListAutomations(t *testing.T) {
	router, store, _ := setupAPI(t)

	require.NoError(t, store.Save(&model.Automation{ID: "f1", Name: "Automation 1", Trigger: model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch1"}}}))
	require.NoError(t, store.Save(&model.Automation{ID: "f2", Name: "Automation 2", Trigger: model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch2"}}}))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/automations", nil)
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var automations []*model.Automation
	require.NoError(t, json.NewDecoder(w.Body).Decode(&automations))
	assert.Len(t, automations, 2)
}

func TestAPI_ListAutomations_Empty(t *testing.T) {
	router, _, _ := setupAPI(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/automations", nil)
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)

	require.Equal(t, http.StatusOK, w.Code)

	var automations []*model.Automation
	require.NoError(t, json.NewDecoder(w.Body).Decode(&automations))
	assert.Empty(t, automations)
}

func TestAPI_UpdateAutomation(t *testing.T) {
	router, store, _ := setupAPI(t)

	original := &model.Automation{
		ID:        "f1",
		Name:      "Original",
		CreatedAt: 1000,
		CreatedBy: "original-user",
		Trigger:   model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch1"}},
	}
	require.NoError(t, store.Save(original))

	body := `{
		"name": "Updated",
		"enabled": true,
		"trigger": {"message_posted": {"channel_id": "ch2"}},
		"actions": [{"id": "new-action", "send_message": {"channel_id": "ch2", "body": "updated"}}]
	}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/automations/f1", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "original-user")

	router.ServeHTTP(w, r)

	require.Equal(t, http.StatusOK, w.Code)

	var updated model.Automation
	require.NoError(t, json.NewDecoder(w.Body).Decode(&updated))
	assert.Equal(t, "f1", updated.ID)
	assert.Equal(t, "Updated", updated.Name)
	assert.True(t, updated.Enabled)
	// Immutable fields preserved.
	assert.Equal(t, int64(1000), updated.CreatedAt)
	assert.Equal(t, "original-user", updated.CreatedBy)
	assert.Greater(t, updated.UpdatedAt, updated.CreatedAt)
	require.Len(t, updated.Actions, 1)
	assert.Equal(t, "new-action", updated.Actions[0].ID)
}

func TestAPI_UpdateAutomation_PreservesEnabledWhenOmitted(t *testing.T) {
	router, store, _ := setupAPI(t)

	require.NoError(t, store.Save(&model.Automation{
		ID:        "f1",
		Name:      "Original",
		Enabled:   true,
		CreatedAt: 1000,
		CreatedBy: "user1",
		Trigger:   model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch1"}},
	}))

	// Payload intentionally omits "enabled" — e.g. a partial PUT from an MCP
	// tool. Before the fix this silently flipped Enabled to false.
	body := `{
		"name": "Updated",
		"trigger": {"message_posted": {"channel_id": "ch1"}},
		"actions": [{"id": "a", "send_message": {"channel_id": "ch1", "body": "hi"}}]
	}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/automations/f1", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)
	require.Equal(t, http.StatusOK, w.Code)

	var updated model.Automation
	require.NoError(t, json.NewDecoder(w.Body).Decode(&updated))
	assert.True(t, updated.Enabled, "omitted enabled must preserve existing value")

	stored, err := store.Get("f1")
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.True(t, stored.Enabled, "persisted Enabled must match preserved value")
}

func TestAPI_UpdateAutomation_ExplicitEnabledFalseDisables(t *testing.T) {
	router, store, _ := setupAPI(t)

	require.NoError(t, store.Save(&model.Automation{
		ID:        "f1",
		Name:      "Original",
		Enabled:   true,
		CreatedAt: 1000,
		CreatedBy: "user1",
		Trigger:   model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch1"}},
	}))

	// An explicit "enabled": false must still disable, so the probe must
	// distinguish a present-but-false value from an absent field.
	body := `{
		"name": "Updated",
		"enabled": false,
		"trigger": {"message_posted": {"channel_id": "ch1"}},
		"actions": [{"id": "a", "send_message": {"channel_id": "ch1", "body": "hi"}}]
	}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/automations/f1", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)
	require.Equal(t, http.StatusOK, w.Code)

	stored, err := store.Get("f1")
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.False(t, stored.Enabled, "explicit enabled=false must disable the automation")
}

func TestAPI_UpdateAutomation_PreservesDisabledWhenOmitted(t *testing.T) {
	router, store, _ := setupAPI(t)

	require.NoError(t, store.Save(&model.Automation{
		ID:        "f1",
		Name:      "Original",
		Enabled:   false,
		CreatedAt: 1000,
		CreatedBy: "user1",
		Trigger:   model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch1"}},
	}))

	body := `{
		"name": "Updated",
		"trigger": {"message_posted": {"channel_id": "ch1"}},
		"actions": [{"id": "a", "send_message": {"channel_id": "ch1", "body": "hi"}}]
	}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/automations/f1", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)
	require.Equal(t, http.StatusOK, w.Code)

	stored, err := store.Get("f1")
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.False(t, stored.Enabled, "omitted enabled must preserve the existing (disabled) value")
}

func TestAPI_UpdateAutomation_AcceptsPATCH(t *testing.T) {
	router, store, _ := setupAPI(t)

	require.NoError(t, store.Save(&model.Automation{
		ID:        "f1",
		Name:      "Original",
		Enabled:   true,
		CreatedAt: 1000,
		CreatedBy: "user1",
		Trigger:   model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch1"}},
	}))

	// PATCH and PUT share the handler — the verb difference is a hint to the
	// caller about partial-update semantics. The same Enabled-preserves-on-omit
	// behavior applies.
	body := `{
		"name": "Patched",
		"trigger": {"message_posted": {"channel_id": "ch1"}},
		"actions": [{"id": "a", "send_message": {"channel_id": "ch1", "body": "hi"}}]
	}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPatch, "/automations/f1", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)
	require.Equal(t, http.StatusOK, w.Code)

	var updated model.Automation
	require.NoError(t, json.NewDecoder(w.Body).Decode(&updated))
	assert.Equal(t, "Patched", updated.Name)
	assert.True(t, updated.Enabled, "PATCH with omitted enabled must preserve existing value")
}

func TestAPI_UpdateAutomation_NotFound(t *testing.T) {
	router, _, _ := setupAPI(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/automations/nonexistent", bytes.NewBufferString(`{"name":"x"}`))
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestAPI_UpdateAutomation_InvalidBody(t *testing.T) {
	router, store, _ := setupAPI(t)

	require.NoError(t, store.Save(&model.Automation{ID: "f1", Trigger: model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch1"}}}))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/automations/f1", bytes.NewBufferString("not json"))
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestAPI_CreateAutomation_ScheduleTrigger_MissingInterval(t *testing.T) {
	router, _, _ := setupAPI(t)

	body := `{
		"name": "Schedule Automation",
		"enabled": true,
		"trigger": {"schedule": {"channel_id": "ch1"}},
		"actions": [{"id": "send-message", "send_message": {"channel_id": "ch2", "body": "hello"}}]
	}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/automations", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "interval")
}

func TestAPI_CreateAutomation_ScheduleTrigger_IntervalTooSmall(t *testing.T) {
	router, _, _ := setupAPI(t)

	body := `{
		"name": "Schedule Automation",
		"enabled": true,
		"trigger": {"schedule": {"channel_id": "ch1", "interval": "30m"}},
		"actions": [{"id": "send-message", "send_message": {"channel_id": "ch2", "body": "hello"}}]
	}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/automations", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "at least 1h")
}

func TestAPI_CreateAutomation_UnknownTriggerType(t *testing.T) {
	router, _, _ := setupAPI(t)

	body := `{
		"name": "Bad Trigger",
		"enabled": true,
		"trigger": {},
		"actions": [{"id": "send-message", "send_message": {"channel_id": "ch2", "body": "hello"}}]
	}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/automations", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestAPI_UpdateAutomation_ScheduleValidation(t *testing.T) {
	router, store, _ := setupAPI(t)

	require.NoError(t, store.Save(&model.Automation{
		ID:        "f1",
		CreatedBy: "user1",
		Trigger:   model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch1"}},
	}))

	body := `{
		"name": "Updated",
		"enabled": true,
		"trigger": {"schedule": {"channel_id": "ch1", "interval": "30m"}},
		"actions": [{"id": "a", "send_message": {"channel_id": "ch1", "body": "hi"}}]
	}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/automations/f1", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "at least 1h")
}

func TestAPI_UpdateAutomation_UnchangedPastStartAt(t *testing.T) {
	router, store, _ := setupAPI(t)

	pastStartAt := model.TimeToTimestamp(time.Now().Add(-1 * time.Hour))
	require.NoError(t, store.Save(&model.Automation{
		ID:        "f1",
		CreatedBy: "user1",
		Trigger:   model.Trigger{Schedule: &model.ScheduleConfig{ChannelID: "ch1", Interval: "2h", StartAt: pastStartAt}},
	}))

	body := fmt.Sprintf(`{
		"name": "Updated name",
		"enabled": true,
		"trigger": {"schedule": {"channel_id": "ch1", "interval": "2h", "start_at": %d}},
		"actions": [{"id": "a", "send_message": {"channel_id": "ch1", "body": "hi"}}]
	}`, pastStartAt)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/automations/f1", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestAPI_DeleteAutomation(t *testing.T) {
	router, store, _ := setupAPI(t)

	require.NoError(t, store.Save(&model.Automation{ID: "f1", CreatedBy: "user1", Trigger: model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch1"}}}))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/automations/f1", nil)
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusNoContent, w.Code)

	// Verify deleted.
	got, err := store.Get("f1")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestAPI_DeleteAutomation_Unauthorized(t *testing.T) {
	router, store, _ := setupAPI(t)

	require.NoError(t, store.Save(&model.Automation{ID: "f1", Trigger: model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch1"}}}))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/automations/f1", nil)
	// Deliberately omit Mattermost-User-ID header.

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestAPI_UpdateAutomation_Unauthorized(t *testing.T) {
	router, store, _ := setupAPI(t)

	require.NoError(t, store.Save(&model.Automation{ID: "f1", Trigger: model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch1"}}}))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/automations/f1", bytes.NewBufferString(`{"name":"x","trigger":{"message_posted":{"channel_id":"ch1"}},"actions":[{"id":"a","send_message":{"channel_id":"ch1","body":"hi"}}]}`))
	// Deliberately omit Mattermost-User-ID header.

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

// setupAPIWithCustomMock creates an API handler with a custom plugintest.API
// so callers can set their own permission-check expectations.
func setupAPIWithCustomMock(t *testing.T, api *plugintest.API) (*mux.Router, model.Store) {
	t.Helper()

	store, _ := setupStore(t)

	handler := NewAPIHandler(store, nil, api, newTestRegistry(), nil, nil, nil)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	return router, store
}

// setupAPIWithLimit creates an API handler with a per-channel automation limit.
func setupAPIWithLimit(t *testing.T, limit int) (*mux.Router, model.Store) {
	t.Helper()

	store, _ := setupStore(t)

	api := &plugintest.API{}
	expectLogCalls(api)
	api.On("HasPermissionTo", mock.Anything, mmmodel.PermissionManageSystem).Return(false).Maybe()
	api.On("GetChannel", mock.Anything).Return(
		&mmmodel.Channel{Type: mmmodel.ChannelTypeOpen}, nil,
	).Maybe()
	api.On("GetChannelMember", mock.Anything, mock.Anything).Return(&mmmodel.ChannelMember{}, nil).Maybe()
	api.On("HasPermissionToChannel", mock.Anything, mock.Anything, mmmodel.PermissionManageChannelRoles).Return(true).Maybe()

	handler := NewAPIHandler(store, nil, api, newTestRegistry(), nil, &testConfig{maxAutomationsPerChannel: limit}, nil)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	return router, store
}

// stubAgentToolsLister implements hooks.AgentToolsLister for API tests. It
// returns a fixed tools/err pair and records every (agent, user) pair the
// validator queried so tests can assert dedup and "skipped entirely" cases.
type stubAgentToolsLister struct {
	tools []bridgeclient.BridgeToolInfo
	err   error
	calls []stubBridgeCall
}

type stubBridgeCall struct {
	agentID string
	userID  string
}

func (s *stubAgentToolsLister) GetAgentTools(agentID, userID string) ([]bridgeclient.BridgeToolInfo, error) {
	s.calls = append(s.calls, stubBridgeCall{agentID: agentID, userID: userID})
	return s.tools, s.err
}

// setupAPIWithBridge creates an API handler with a custom plugintest.API and
// a stub bridge so tests can exercise save-time agent access verification.
func setupAPIWithBridge(t *testing.T, api *plugintest.API, bridge *stubAgentToolsLister) (*mux.Router, model.Store) {
	t.Helper()

	store, _ := setupStore(t)

	handler := NewAPIHandler(store, nil, api, newTestRegistry(), nil, nil, bridge)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	return router, store
}

func TestAPI_CreateAutomation_PermissionDenied(t *testing.T) {
	api := &plugintest.API{}
	expectLogCalls(api)
	api.On("HasPermissionTo", "user1", mmmodel.PermissionManageSystem).Return(false)
	api.On("GetChannel", "ch1").Return(
		&mmmodel.Channel{Id: "ch1", TeamId: "team1", Type: mmmodel.ChannelTypeOpen}, nil,
	)
	api.On("GetChannelMember", "ch1", "user1").Return(&mmmodel.ChannelMember{}, nil)
	api.On("HasPermissionToChannel", "user1", "ch1", mmmodel.PermissionManageChannelRoles).Return(false)

	router, _ := setupAPIWithCustomMock(t, api)

	body := `{
		"name": "Test Automation",
		"enabled": true,
		"trigger": {"message_posted": {"channel_id": "ch1"}},
		"actions": [{"id": "send-message", "send_message": {"channel_id": "ch1", "body": "hello"}}]
	}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/automations", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "permission to manage")
	api.AssertExpectations(t)
}

func TestAPI_CreateAutomation_TeamAdminWithoutChannelAdminAllowed(t *testing.T) {
	api := &plugintest.API{}
	expectLogCalls(api)
	api.On("HasPermissionTo", "user1", mmmodel.PermissionManageSystem).Return(false)
	api.On("GetChannel", "ch1").Return(
		&mmmodel.Channel{Id: "ch1", TeamId: "team1", Type: mmmodel.ChannelTypeOpen}, nil,
	)
	api.On("GetChannelMember", "ch1", "user1").Return(&mmmodel.ChannelMember{}, nil)
	api.On("HasPermissionToChannel", "user1", "ch1", mmmodel.PermissionManageChannelRoles).Return(true)

	router, _ := setupAPIWithCustomMock(t, api)

	body := `{
		"name": "Team Admin Automation",
		"enabled": true,
		"trigger": {"message_posted": {"channel_id": "ch1"}},
		"actions": [{"id": "send-message", "send_message": {"channel_id": "ch1", "body": "hello"}}]
	}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/automations", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusCreated, w.Code)
	api.AssertExpectations(t)
}

func TestAPI_CreateAutomation_ActionPermissionDenied(t *testing.T) {
	api := &plugintest.API{}
	expectLogCalls(api)
	api.On("HasPermissionTo", "user1", mmmodel.PermissionManageSystem).Return(false)
	api.On("GetChannel", "ch1").Return(
		&mmmodel.Channel{Id: "ch1", Type: mmmodel.ChannelTypeOpen}, nil,
	)
	api.On("GetChannelMember", "ch1", "user1").Return(&mmmodel.ChannelMember{}, nil)
	api.On("HasPermissionToChannel", "user1", "ch1", mmmodel.PermissionManageChannelRoles).Return(false)

	router, _ := setupAPIWithCustomMock(t, api)

	body := `{
		"name": "Test Automation",
		"enabled": true,
		"trigger": {"message_posted": {"channel_id": "ch1"}},
		"actions": [{"id": "send-message", "send_message": {"channel_id": "ch1", "body": "hello"}}]
	}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/automations", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "permission to manage")
}

func TestAPI_CreateAutomation_NotChannelMember(t *testing.T) {
	api := &plugintest.API{}
	expectLogCalls(api)
	api.On("HasPermissionTo", "user1", mmmodel.PermissionManageSystem).Return(false)
	api.On("GetChannel", "ch1").Return(
		&mmmodel.Channel{Id: "ch1", Type: mmmodel.ChannelTypeOpen}, nil,
	)
	api.On("GetChannelMember", "ch1", "user1").Return(nil, &mmmodel.AppError{
		Message:    "channel member not found",
		StatusCode: http.StatusNotFound,
	})

	router, _ := setupAPIWithCustomMock(t, api)

	body := `{
		"name": "Test Automation",
		"enabled": true,
		"trigger": {"message_posted": {"channel_id": "ch1"}},
		"actions": [{"id": "send-message", "send_message": {"channel_id": "ch1", "body": "hello"}}]
	}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/automations", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "permission to manage")
}

func TestAPI_UpdateAutomation_PermissionDenied(t *testing.T) {
	api := &plugintest.API{}
	expectLogCalls(api)
	api.On("HasPermissionTo", "user1", mmmodel.PermissionManageSystem).Return(false)
	api.On("GetChannel", "ch1").Return(
		&mmmodel.Channel{Id: "ch1", Type: mmmodel.ChannelTypeOpen}, nil,
	)
	api.On("GetChannelMember", "ch1", "user1").Return(&mmmodel.ChannelMember{}, nil)
	api.On("HasPermissionToChannel", "user1", "ch1", mmmodel.PermissionManageChannelRoles).Return(true)
	api.On("GetChannel", "ch-new").Return(
		&mmmodel.Channel{Id: "ch-new", Type: mmmodel.ChannelTypeOpen}, nil,
	)
	api.On("GetChannelMember", "ch-new", "user1").Return(&mmmodel.ChannelMember{}, nil)
	api.On("HasPermissionToChannel", "user1", "ch-new", mmmodel.PermissionManageChannelRoles).Return(false)

	router, store := setupAPIWithCustomMock(t, api)

	require.NoError(t, store.Save(&model.Automation{
		ID:        "f1",
		Name:      "Original",
		CreatedBy: "user1",
		Trigger:   model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch1"}},
	}))

	body := `{
		"name": "Updated",
		"enabled": true,
		"trigger": {"message_posted": {"channel_id": "ch-new"}},
		"actions": [{"id": "send-msg", "send_message": {"channel_id": "ch-new", "body": "hi"}}]
	}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/automations/f1", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "permission to manage")
}

func TestAPI_UpdateAutomation_NonCreatorRejected(t *testing.T) {
	api := &plugintest.API{}
	expectLogCalls(api)
	api.On("HasPermissionTo", "user2", mmmodel.PermissionManageSystem).Return(false)

	router, store := setupAPIWithCustomMock(t, api)

	require.NoError(t, store.Save(&model.Automation{
		ID:        "f1",
		Name:      "Original",
		CreatedBy: "user1",
		Trigger:   model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch1"}},
	}))

	body := `{
		"name": "Updated",
		"enabled": true,
		"trigger": {"message_posted": {"channel_id": "ch1"}},
		"actions": [{"id": "send-msg", "send_message": {"channel_id": "ch1", "body": "hi"}}]
	}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/automations/f1", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "user2")

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "automation creator or a system admin")
	api.AssertNotCalled(t, "HasPermissionToChannel", mock.Anything, mock.Anything, mock.Anything)
}

func TestAPI_CreateAutomation_SystemAdminBypass(t *testing.T) {
	api := &plugintest.API{}
	expectLogCalls(api)
	api.On("HasPermissionTo", "admin1", mmmodel.PermissionManageSystem).Return(true)
	api.On("GetChannel", "ch1").Return(
		&mmmodel.Channel{Id: "ch1", Type: mmmodel.ChannelTypeOpen}, nil,
	)
	api.On("GetChannelMember", "ch1", "admin1").Return(&mmmodel.ChannelMember{}, nil)
	// Sysadmins skip manage_channel_roles but must still be channel members.

	router, _ := setupAPIWithCustomMock(t, api)

	body := `{
		"name": "Admin Automation",
		"enabled": true,
		"trigger": {"message_posted": {"channel_id": "ch1"}},
		"actions": [{"id": "send-message", "send_message": {"channel_id": "ch1", "body": "hello"}}]
	}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/automations", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "admin1")

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusCreated, w.Code)

	api.AssertNotCalled(t, "HasPermissionToChannel", mock.Anything, mock.Anything, mock.Anything)
}

func TestAPI_UpdateAutomation_SystemAdminMemberAllowed(t *testing.T) {
	api := &plugintest.API{}
	expectLogCalls(api)
	api.On("HasPermissionTo", "admin1", mmmodel.PermissionManageSystem).Return(true)
	api.On("HasPermissionTo", "creator1", mmmodel.PermissionManageSystem).Return(true)
	api.On("GetChannel", "ch-new").Return(
		&mmmodel.Channel{Id: "ch-new", Type: mmmodel.ChannelTypeOpen}, nil,
	)
	api.On("GetChannelMember", "ch-new", "admin1").Return(&mmmodel.ChannelMember{}, nil)
	api.On("GetChannelMember", "ch-new", "creator1").Return(&mmmodel.ChannelMember{}, nil)

	router, store := setupAPIWithCustomMock(t, api)

	require.NoError(t, store.Save(&model.Automation{
		ID:        "f1",
		Name:      "Original",
		CreatedBy: "creator1",
		Trigger:   model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch1"}},
	}))

	body := `{
		"name": "Updated by admin",
		"enabled": true,
		"trigger": {"message_posted": {"channel_id": "ch-new"}},
		"actions": [{"id": "send-message", "send_message": {"channel_id": "ch-new", "body": "hello"}}]
	}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/automations/f1", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "admin1")

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusOK, w.Code)

	api.AssertNotCalled(t, "HasPermissionToChannel", mock.Anything, mock.Anything, mock.Anything)
}

func TestAPI_UpdateAutomation_SystemAdminNotMemberDenied(t *testing.T) {
	api := &plugintest.API{}
	expectLogCalls(api)
	api.On("HasPermissionTo", "admin1", mmmodel.PermissionManageSystem).Return(true)
	api.On("GetChannel", "ch-new").Return(
		&mmmodel.Channel{Id: "ch-new", Type: mmmodel.ChannelTypeOpen}, nil,
	)
	api.On("GetChannelMember", "ch-new", "admin1").Return(nil, &mmmodel.AppError{
		Message:    "channel member not found",
		StatusCode: http.StatusNotFound,
	})

	router, store := setupAPIWithCustomMock(t, api)

	require.NoError(t, store.Save(&model.Automation{
		ID:        "f1",
		Name:      "Original",
		CreatedBy: "creator1",
		Trigger:   model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch1"}},
	}))

	body := `{
		"name": "Updated by admin",
		"enabled": true,
		"trigger": {"message_posted": {"channel_id": "ch-new"}},
		"actions": [{"id": "send-message", "send_message": {"channel_id": "ch-new", "body": "hello"}}]
	}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/automations/f1", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "admin1")

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "permission to manage")
	api.AssertNotCalled(t, "HasPermissionToChannel", mock.Anything, mock.Anything, mock.Anything)
}

func TestAPI_CreateAutomation_TemplatedChannelSkipped(t *testing.T) {
	api := &plugintest.API{}
	expectLogCalls(api)
	api.On("HasPermissionTo", "user1", mmmodel.PermissionManageSystem).Return(false)
	// Only the trigger channel should be checked; the templated action channel is skipped.
	api.On("GetChannel", "ch1").Return(
		&mmmodel.Channel{Id: "ch1", Type: mmmodel.ChannelTypeOpen}, nil,
	)
	api.On("GetChannelMember", "ch1", "user1").Return(&mmmodel.ChannelMember{}, nil)
	api.On("HasPermissionToChannel", "user1", "ch1", mmmodel.PermissionManageChannelRoles).Return(true)

	router, _ := setupAPIWithCustomMock(t, api)

	body := `{
		"name": "Test Automation",
		"enabled": true,
		"trigger": {"message_posted": {"channel_id": "ch1"}},
		"actions": [{"id": "reply-echo", "send_message": {"channel_id": "{{.Trigger.Channel.Id}}", "body": "echo"}}]
	}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/automations", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusCreated, w.Code)

	api.AssertNumberOfCalls(t, "HasPermissionToChannel", 1)
}

func TestAPI_CreateAutomation_ChannelCreated_NonTeamAdminDenied(t *testing.T) {
	api := &plugintest.API{}
	expectLogCalls(api)
	api.On("HasPermissionTo", "user1", mmmodel.PermissionManageSystem).Return(false)
	api.On("GetTeam", "team1").Return(&mmmodel.Team{Id: "team1"}, nil)
	api.On("HasPermissionToTeam", "user1", "team1", mmmodel.PermissionManageTeam).Return(false)

	router, _ := setupAPIWithCustomMock(t, api)

	body := `{
		"name": "Team Automation",
		"enabled": true,
		"trigger": {"channel_created": {"team_id": "team1"}},
		"actions": [{"id": "announce", "send_message": {"channel_id": "{{.Trigger.Channel.Id}}", "body": "hello"}}]
	}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/automations", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "team admin")
}

func TestAPI_CreateAutomation_ChannelCreated_AIPromptOnly_NonTeamAdminDenied(t *testing.T) {
	api := &plugintest.API{}
	expectLogCalls(api)
	api.On("HasPermissionTo", "user1", mmmodel.PermissionManageSystem).Return(false)
	api.On("GetTeam", "team1").Return(&mmmodel.Team{Id: "team1"}, nil)
	api.On("HasPermissionToTeam", "user1", "team1", mmmodel.PermissionManageTeam).Return(false)

	router, _ := setupAPIWithCustomMock(t, api)

	api.On("GetUser", "user1").Return(&mmmodel.User{Id: "user1", IsBot: false}, (*mmmodel.AppError)(nil)).Maybe()

	body := `{
		"name": "AI on new channels",
		"enabled": true,
		"trigger": {"channel_created": {"team_id": "team1"}},
		"actions": [{"id": "ai-task", "ai_prompt": {"prompt": "summarize", "provider_type": "agent", "provider_id": "bot1", "request_as": "creator"}}]
	}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/automations", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "team admin")
}

func TestAPI_CreateAutomation_ChannelCreated_SystemAdminAllowed(t *testing.T) {
	api := &plugintest.API{}
	expectLogCalls(api)
	api.On("HasPermissionTo", "admin1", mmmodel.PermissionManageSystem).Return(true)

	router, _ := setupAPIWithCustomMock(t, api)

	body := `{
		"name": "Team Automation",
		"enabled": true,
		"trigger": {"channel_created": {"team_id": "team1"}},
		"actions": [{"id": "announce", "send_message": {"channel_id": "{{.Trigger.Channel.Id}}", "body": "hello"}}]
	}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/automations", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "admin1")

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusCreated, w.Code)
}

func TestAPI_CreateAutomation_ChannelCreated_TeamAdminAllowed(t *testing.T) {
	api := &plugintest.API{}
	expectLogCalls(api)
	api.On("HasPermissionTo", "user1", mmmodel.PermissionManageSystem).Return(false)
	api.On("GetTeam", "team1").Return(&mmmodel.Team{Id: "team1"}, nil)
	api.On("HasPermissionToTeam", "user1", "team1", mmmodel.PermissionManageTeam).Return(true)

	router, _ := setupAPIWithCustomMock(t, api)

	body := `{
		"name": "Team Automation",
		"enabled": true,
		"trigger": {"channel_created": {"team_id": "team1"}},
		"actions": [{"id": "announce", "send_message": {"channel_id": "{{.Trigger.Channel.Id}}", "body": "hello"}}]
	}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/automations", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusCreated, w.Code)
}

func TestAPI_CreateAutomation_ChannelCreated_LiteralChannelRejected(t *testing.T) {
	// With the temporary channel guardrail, channel_created triggers must use
	// the template expression for send_message channel_id.
	router, _, _ := setupAPI(t)

	body := `{
		"name": "Team Automation",
		"enabled": true,
		"trigger": {"channel_created": {"team_id": "team1"}},
		"actions": [{"id": "announce", "send_message": {"channel_id": "ch-other", "body": "hello"}}]
	}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/automations", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "must use a template expression")
}

func TestAPI_ListAutomations_ChannelCreated_HiddenFromNonTeamAdmin(t *testing.T) {
	api := &plugintest.API{}
	expectLogCalls(api)
	api.On("HasPermissionTo", "user1", mmmodel.PermissionManageSystem).Return(false)
	api.On("GetChannel", "ch1").Return(
		&mmmodel.Channel{Id: "ch1", Type: mmmodel.ChannelTypeOpen}, nil,
	)
	api.On("GetChannelMember", "ch1", "user1").Return(&mmmodel.ChannelMember{}, nil)
	api.On("HasPermissionToChannel", "user1", "ch1", mmmodel.PermissionManageChannelRoles).Return(true)
	api.On("GetTeam", "team1").Return(&mmmodel.Team{Id: "team1"}, nil)
	api.On("HasPermissionToTeam", "user1", "team1", mmmodel.PermissionManageTeam).Return(false)

	router, store := setupAPIWithCustomMock(t, api)

	// A normal automation the user can see.
	require.NoError(t, store.Save(&model.Automation{
		ID:      "f1",
		Name:    "Normal Automation",
		Trigger: model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch1"}},
	}))
	// A channel_created automation — should be hidden from non-team-admin.
	require.NoError(t, store.Save(&model.Automation{
		ID:      "f2",
		Name:    "Team Automation",
		Trigger: model.Trigger{ChannelCreated: &model.ChannelCreatedConfig{TeamID: "team1"}},
	}))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/automations", nil)
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)

	require.Equal(t, http.StatusOK, w.Code)

	var automations []*model.Automation
	require.NoError(t, json.NewDecoder(w.Body).Decode(&automations))
	require.Len(t, automations, 1)
	assert.Equal(t, "f1", automations[0].ID)
}

func TestAPI_ListAutomations_FilterByChannel(t *testing.T) {
	router, store, _ := setupAPI(t)

	require.NoError(t, store.Save(&model.Automation{ID: "f1", Name: "Automation 1", Trigger: model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch1"}}}))
	require.NoError(t, store.Save(&model.Automation{ID: "f2", Name: "Automation 2", Trigger: model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch2"}}}))
	require.NoError(t, store.Save(&model.Automation{ID: "f3", Name: "Automation 3", Trigger: model.Trigger{Schedule: &model.ScheduleConfig{ChannelID: "ch1", Interval: "1h"}}}))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/automations?channel_id=ch1", nil)
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)

	require.Equal(t, http.StatusOK, w.Code)

	var automations []*model.Automation
	require.NoError(t, json.NewDecoder(w.Body).Decode(&automations))
	require.Len(t, automations, 2)
	ids := []string{automations[0].ID, automations[1].ID}
	assert.ElementsMatch(t, []string{"f1", "f3"}, ids)
}

func TestAPI_ListAutomations_FilterByChannel_NoMatch(t *testing.T) {
	router, store, _ := setupAPI(t)

	require.NoError(t, store.Save(&model.Automation{ID: "f1", Name: "Automation 1", Trigger: model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch1"}}}))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/automations?channel_id=ch-nonexistent", nil)
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)

	require.Equal(t, http.StatusOK, w.Code)

	var automations []*model.Automation
	require.NoError(t, json.NewDecoder(w.Body).Decode(&automations))
	assert.Empty(t, automations)
}

func TestAPI_CreateAutomation_ChannelLimitReached(t *testing.T) {
	router, store := setupAPIWithLimit(t, 1)

	// Save one automation on ch1 — fills the limit.
	require.NoError(t, store.Save(&model.Automation{
		ID:      "f1",
		Trigger: model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch1"}},
	}))

	body := `{
		"name": "Second Automation",
		"enabled": true,
		"trigger": {"message_posted": {"channel_id": "ch1"}},
		"actions": [{"id": "send-msg", "send_message": {"channel_id": "ch1", "body": "hello"}}]
	}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/automations", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Contains(t, w.Body.String(), "maximum")
}

func TestAPI_CreateAutomation_DifferentChannelSucceeds(t *testing.T) {
	router, store := setupAPIWithLimit(t, 1)

	// ch1 is full.
	require.NoError(t, store.Save(&model.Automation{
		ID:      "f1",
		Trigger: model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch1"}},
	}))

	// Creating on ch2 should succeed.
	body := `{
		"name": "Other Channel Automation",
		"enabled": true,
		"trigger": {"message_posted": {"channel_id": "ch2"}},
		"actions": [{"id": "send-msg", "send_message": {"channel_id": "ch2", "body": "hello"}}]
	}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/automations", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusCreated, w.Code)
}

func TestAPI_UpdateAutomation_SameChannelSelfExclusion(t *testing.T) {
	router, store := setupAPIWithLimit(t, 1)

	// ch1 has one automation — at the limit.
	require.NoError(t, store.Save(&model.Automation{
		ID:        "f1",
		Name:      "Original",
		CreatedBy: "user1",
		Trigger:   model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch1"}},
	}))

	// Updating the same automation on the same channel should succeed (self-exclusion).
	body := `{
		"name": "Updated",
		"enabled": true,
		"trigger": {"message_posted": {"channel_id": "ch1"}},
		"actions": [{"id": "send-msg", "send_message": {"channel_id": "ch1", "body": "updated"}}]
	}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/automations/f1", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestAPI_UpdateAutomation_MoveToFullChannel(t *testing.T) {
	router, store := setupAPIWithLimit(t, 1)

	// Each of ch1 and ch2 already has one automation; with limit=1, ch2 is full.
	require.NoError(t, store.Save(&model.Automation{
		ID:        "f1",
		CreatedBy: "user1",
		Trigger:   model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch1"}},
	}))
	require.NoError(t, store.Save(&model.Automation{
		ID:        "f2",
		CreatedBy: "user1",
		Trigger:   model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch2"}},
	}))

	// Moving f1 to ch2 should be blocked (ch2 already at limit).
	body := `{
		"name": "Moved",
		"enabled": true,
		"trigger": {"message_posted": {"channel_id": "ch2"}},
		"actions": [{"id": "send-msg", "send_message": {"channel_id": "ch2", "body": "moved"}}]
	}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/automations/f1", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Contains(t, w.Body.String(), "maximum")
}

func TestAPI_CreateAutomation_UnlimitedAllowsAny(t *testing.T) {
	router, store := setupAPIWithLimit(t, 0)

	// Even with many automations, limit=0 means unlimited.
	require.NoError(t, store.Save(&model.Automation{
		ID:      "f1",
		Trigger: model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch1"}},
	}))
	require.NoError(t, store.Save(&model.Automation{
		ID:      "f2",
		Trigger: model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch1"}},
	}))

	body := `{
		"name": "Third Automation",
		"enabled": true,
		"trigger": {"message_posted": {"channel_id": "ch1"}},
		"actions": [{"id": "send-msg", "send_message": {"channel_id": "ch1", "body": "hello"}}]
	}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/automations", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusCreated, w.Code)
}

func TestAPI_CreateAutomation_ChannelCreatedBypassesLimit(t *testing.T) {
	api := &plugintest.API{}
	expectLogCalls(api)
	api.On("HasPermissionTo", "admin1", mmmodel.PermissionManageSystem).Return(true)

	store, _ := setupStore(t)

	handler := NewAPIHandler(store, nil, api, newTestRegistry(), nil, &testConfig{maxAutomationsPerChannel: 1}, nil)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	// channel_created automations have no trigger channel, so they bypass the limit.
	body := `{
		"name": "Team Automation",
		"enabled": true,
		"trigger": {"channel_created": {"team_id": "team1"}},
		"actions": [{"id": "announce", "send_message": {"channel_id": "{{.Trigger.Channel.Id}}", "body": "hello"}}]
	}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/automations", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "admin1")

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusCreated, w.Code)
}

func TestAPI_CreateAutomation_EmptyName(t *testing.T) {
	router, _, _ := setupAPI(t)

	body := `{
		"name": "",
		"enabled": true,
		"trigger": {"message_posted": {"channel_id": "ch1"}},
		"actions": [{"id": "send-msg", "send_message": {"channel_id": "ch1", "body": "hello"}}]
	}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/automations", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "name is required")
}

func TestAPI_CreateAutomation_NameTooLong(t *testing.T) {
	router, _, _ := setupAPI(t)

	longName := strings.Repeat("a", 101)
	body := fmt.Sprintf(`{
		"name": %q,
		"enabled": true,
		"trigger": {"message_posted": {"channel_id": "ch1"}},
		"actions": [{"id": "send-msg", "send_message": {"channel_id": "ch1", "body": "hello"}}]
	}`, longName)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/automations", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "100 characters")
}

func TestAPI_UpdateAutomation_EmptyName(t *testing.T) {
	router, store, _ := setupAPI(t)

	require.NoError(t, store.Save(&model.Automation{
		ID:        "f1",
		Name:      "Original",
		CreatedBy: "user1",
		Trigger:   model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch1"}},
	}))

	body := `{
		"name": "",
		"enabled": true,
		"trigger": {"message_posted": {"channel_id": "ch1"}},
		"actions": [{"id": "a", "send_message": {"channel_id": "ch1", "body": "hi"}}]
	}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/automations/f1", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "name is required")
}

func TestAPI_UpdateAutomation_NameTooLong(t *testing.T) {
	router, store, _ := setupAPI(t)

	require.NoError(t, store.Save(&model.Automation{
		ID:        "f1",
		Name:      "Original",
		CreatedBy: "user1",
		Trigger:   model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch1"}},
	}))

	longName := strings.Repeat("a", 101)
	body := fmt.Sprintf(`{
		"name": %q,
		"enabled": true,
		"trigger": {"message_posted": {"channel_id": "ch1"}},
		"actions": [{"id": "a", "send_message": {"channel_id": "ch1", "body": "hi"}}]
	}`, longName)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/automations/f1", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "100 characters")
}

func TestAPI_CreateAutomation_EmptyActions(t *testing.T) {
	router, _, _ := setupAPI(t)

	body := `{
		"name": "Test Automation",
		"enabled": true,
		"trigger": {"message_posted": {"channel_id": "ch1"}},
		"actions": []
	}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/automations", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "at least one action")
}

func TestAPI_CreateAutomation_MultipleTriggerTypes(t *testing.T) {
	router, _, _ := setupAPI(t)

	body := `{
		"name": "Test Automation",
		"enabled": true,
		"trigger": {"message_posted": {"channel_id": "ch1"}, "schedule": {"channel_id": "ch1", "interval": "2h"}},
		"actions": [{"id": "send-msg", "send_message": {"channel_id": "ch1", "body": "hello"}}]
	}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/automations", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "exactly one trigger type")
}

func TestAPI_CreateAutomation_UserJoinedTeam_TeamAdminAllowed(t *testing.T) {
	api := &plugintest.API{}
	expectLogCalls(api)
	api.On("HasPermissionTo", "user1", mmmodel.PermissionManageSystem).Return(false)
	api.On("GetTeam", "team1").Return(&mmmodel.Team{Id: "team1"}, nil)
	api.On("HasPermissionToTeam", "user1", "team1", mmmodel.PermissionManageTeam).Return(true)

	router, _ := setupAPIWithCustomMock(t, api)

	body := `{
		"name": "Team Join Automation",
		"enabled": true,
		"trigger": {"user_joined_team": {"team_id": "team1"}},
		"actions": [{"id": "greet", "send_message": {"channel_id": "{{.Trigger.Team.DefaultChannelId}}", "body": "welcome"}}]
	}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/automations", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusCreated, w.Code)
}

func TestAPI_CreateAutomation_UserJoinedTeam_NotTeamAdminDenied(t *testing.T) {
	api := &plugintest.API{}
	expectLogCalls(api)
	api.On("HasPermissionTo", "user1", mmmodel.PermissionManageSystem).Return(false)
	api.On("GetTeam", "team1").Return(&mmmodel.Team{Id: "team1"}, nil)
	api.On("HasPermissionToTeam", "user1", "team1", mmmodel.PermissionManageTeam).Return(false)

	router, _ := setupAPIWithCustomMock(t, api)

	body := `{
		"name": "Team Join Automation",
		"enabled": true,
		"trigger": {"user_joined_team": {"team_id": "team1"}},
		"actions": [{"id": "greet", "send_message": {"channel_id": "{{.Trigger.Team.DefaultChannelId}}", "body": "welcome"}}]
	}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/automations", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "team admin")
}

func TestAPI_CreateAutomation_UserJoinedTeam_GetTeam500(t *testing.T) {
	api := &plugintest.API{}
	expectLogCalls(api)
	api.On("HasPermissionTo", "user1", mmmodel.PermissionManageSystem).Return(false)
	api.On("GetTeam", "team1").Return(
		nil, &mmmodel.AppError{StatusCode: http.StatusInternalServerError, Message: "db down"},
	)

	router, _ := setupAPIWithCustomMock(t, api)

	body := `{
		"name": "Team Join Automation",
		"enabled": true,
		"trigger": {"user_joined_team": {"team_id": "team1"}},
		"actions": [{"id": "greet", "send_message": {"channel_id": "{{.Trigger.Team.DefaultChannelId}}", "body": "welcome"}}]
	}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/automations", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// adminAPIWithBridge wires a sysadmin actor (so channel/team checks are
// skipped) plus a stub bridge, so the only relevant assertion in the test
// body is the bridge's own behavior.
func adminAPIWithBridge(t *testing.T, userID string, bridge *stubAgentToolsLister) (*mux.Router, model.Store, *plugintest.API) {
	t.Helper()
	api := &plugintest.API{}
	expectLogCalls(api)
	api.On("HasPermissionTo", userID, mmmodel.PermissionManageSystem).Return(true)
	api.On("GetUser", userID).Return(&mmmodel.User{Id: userID, IsBot: false}, (*mmmodel.AppError)(nil)).Maybe()
	router, store := setupAPIWithBridge(t, api, bridge)
	return router, store, api
}

const aiAgentAutomationBody = `{
	"name": "AI Automation",
	"enabled": true,
	"trigger": {"channel_created": {"team_id": "team1"}},
	"actions": [{"id": "ai-task", "ai_prompt": {"prompt": "summarize", "provider_type": "agent", "provider_id": "bot1", "request_as": "creator"}}]
}`

// TestAPI_CreateAutomation_AIPromptAgent_NoAllowedTools_ChecksBridge is the core
// regression guard: an automation with provider_type "agent" and empty allowed_tools
// must trigger a bridge call to verify the creator has access to the agent.
func TestAPI_CreateAutomation_AIPromptAgent_NoAllowedTools_ChecksBridge(t *testing.T) {
	bridge := &stubAgentToolsLister{tools: []bridgeclient.BridgeToolInfo{}}
	router, _, _ := adminAPIWithBridge(t, "admin1", bridge)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/automations", bytes.NewBufferString(aiAgentAutomationBody))
	r.Header.Set("Mattermost-User-ID", "admin1")
	router.ServeHTTP(w, r)

	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	require.Len(t, bridge.calls, 1)
	assert.Equal(t, "bot1", bridge.calls[0].agentID)
	assert.Equal(t, "admin1", bridge.calls[0].userID)
}

// TestAPI_CreateAutomation_AIPromptAgent_AccessDenied surfaces a bridge denial as
// 502 (ErrToolDiscovery -> http.StatusBadGateway in the handler).
func TestAPI_CreateAutomation_AIPromptAgent_AccessDenied(t *testing.T) {
	bridge := &stubAgentToolsLister{err: fmt.Errorf("request failed with status 403: permission denied")}
	router, _, _ := adminAPIWithBridge(t, "admin1", bridge)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/automations", bytes.NewBufferString(aiAgentAutomationBody))
	r.Header.Set("Mattermost-User-ID", "admin1")
	router.ServeHTTP(w, r)

	require.Equal(t, http.StatusBadGateway, w.Code)
	assert.Contains(t, w.Body.String(), `failed to list tools for agent \"bot1\"`)
	assert.Contains(t, w.Body.String(), "status 403")
}

// TestAPI_CreateAutomation_AIPromptAgent_BridgeUnavailable rejects when the bridge
// is nil and the automation has an ai_prompt agent action.
func TestAPI_CreateAutomation_AIPromptAgent_BridgeUnavailable(t *testing.T) {
	api := &plugintest.API{}
	expectLogCalls(api)
	api.On("HasPermissionTo", "admin1", mmmodel.PermissionManageSystem).Return(true)
	api.On("GetUser", "admin1").Return(&mmmodel.User{Id: "admin1", IsBot: false}, (*mmmodel.AppError)(nil)).Maybe()

	router, _ := setupAPIWithCustomMock(t, api) // bridge is nil

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/automations", bytes.NewBufferString(aiAgentAutomationBody))
	r.Header.Set("Mattermost-User-ID", "admin1")
	router.ServeHTTP(w, r)

	require.Equal(t, http.StatusBadGateway, w.Code)
	assert.Contains(t, w.Body.String(), "bridge client unavailable")
}

// TestAPI_CreateAutomation_AIPromptService_SkipsBridge confirms service providers
// do not trigger any bridge call (out of scope for the agent ACL).
func TestAPI_CreateAutomation_AIPromptService_SkipsBridge(t *testing.T) {
	bridge := &stubAgentToolsLister{}
	router, _, _ := adminAPIWithBridge(t, "admin1", bridge)

	body := `{
		"name": "Service Automation",
		"enabled": true,
		"trigger": {"channel_created": {"team_id": "team1"}},
		"actions": [{"id": "ai-task", "ai_prompt": {"prompt": "summarize", "provider_type": "service", "provider_id": "openai", "request_as": "creator"}}]
	}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/automations", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "admin1")
	router.ServeHTTP(w, r)

	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	assert.Empty(t, bridge.calls)
}

// TestAPI_CreateAutomation_AIPromptAgent_DuplicateID_DedupesCalls is the property
// that prevents a "two requests" regression: two ai_prompt actions sharing
// one agent ID must result in exactly one bridge call.
func TestAPI_CreateAutomation_AIPromptAgent_DuplicateID_DedupesCalls(t *testing.T) {
	bridge := &stubAgentToolsLister{tools: []bridgeclient.BridgeToolInfo{
		{Name: "search", ServerOrigin: "external-mcp"},
	}}
	router, _, api := adminAPIWithBridge(t, "admin1", bridge)

	// Trigger is channel_created so allowed_tools requires guardrails: stub
	// the channel-permission checks for the guardrail channel.
	guardrailChannelID := mmmodel.NewId()
	api.On("GetChannel", guardrailChannelID).Return(&mmmodel.Channel{Id: guardrailChannelID, TeamId: "team1", Type: mmmodel.ChannelTypeOpen}, (*mmmodel.AppError)(nil))
	api.On("GetChannelMember", guardrailChannelID, "admin1").Return(&mmmodel.ChannelMember{ChannelId: guardrailChannelID, UserId: "admin1"}, (*mmmodel.AppError)(nil))

	body := fmt.Sprintf(`{
		"name": "AI Automation",
		"enabled": true,
		"trigger": {"channel_created": {"team_id": "team1"}},
		"actions": [
			{"id": "with-tools", "ai_prompt": {"prompt": "p", "provider_type": "agent", "provider_id": "bot1", "request_as": "creator", "allowed_tools": ["search"], "guardrails": {"channel_ids": [%q]}}},
			{"id": "without-tools", "ai_prompt": {"prompt": "p", "provider_type": "agent", "provider_id": "bot1", "request_as": "creator"}}
		]
	}`, guardrailChannelID)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/automations", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "admin1")
	router.ServeHTTP(w, r)

	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	require.Len(t, bridge.calls, 1, "bridge must be called exactly once for repeated agent ID")
	assert.Equal(t, "bot1", bridge.calls[0].agentID)
}

// TestAPI_CreateAutomation_AIPromptAgent_PermissionFailureSkipsBridge verifies the
// permission check still short-circuits before the bridge round-trip.
func TestAPI_CreateAutomation_AIPromptAgent_PermissionFailureSkipsBridge(t *testing.T) {
	api := &plugintest.API{}
	expectLogCalls(api)
	api.On("HasPermissionTo", "user1", mmmodel.PermissionManageSystem).Return(false)
	api.On("GetTeam", "team1").Return(&mmmodel.Team{Id: "team1"}, nil)
	api.On("HasPermissionToTeam", "user1", "team1", mmmodel.PermissionManageTeam).Return(false)
	api.On("GetUser", "user1").Return(&mmmodel.User{Id: "user1", IsBot: false}, (*mmmodel.AppError)(nil)).Maybe()

	bridge := &stubAgentToolsLister{}
	router, _ := setupAPIWithBridge(t, api, bridge)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/automations", bytes.NewBufferString(aiAgentAutomationBody))
	r.Header.Set("Mattermost-User-ID", "user1")
	router.ServeHTTP(w, r)

	require.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "team admin")
	assert.Empty(t, bridge.calls, "bridge must not be called when the user cannot manage the automation")
}

// TestAPI_UpdateAutomation_AIPromptAgent_AccessDenied mirrors the create path for
// the PUT handler. The check uses the existing automation's CreatedBy (matching
// the runtime model where the bridge ACL is checked against created_by).
func TestAPI_UpdateAutomation_AIPromptAgent_AccessDenied(t *testing.T) {
	bridge := &stubAgentToolsLister{err: fmt.Errorf("request failed with status 403: permission denied")}

	api := &plugintest.API{}
	expectLogCalls(api)
	api.On("HasPermissionTo", "creator1", mmmodel.PermissionManageSystem).Return(true)
	api.On("GetUser", "creator1").Return(&mmmodel.User{Id: "creator1", IsBot: false}, (*mmmodel.AppError)(nil)).Maybe()

	router, store := setupAPIWithBridge(t, api, bridge)

	require.NoError(t, store.Save(&model.Automation{
		ID:        "f1",
		Name:      "Original",
		CreatedBy: "creator1",
		Trigger:   model.Trigger{ChannelCreated: &model.ChannelCreatedConfig{TeamID: "team1"}},
		Actions: []model.Action{{
			ID:       "ai-task",
			AIPrompt: &model.AIPromptActionConfig{Prompt: "p", ProviderType: "agent", ProviderID: "bot1", RequestAs: "creator"},
		}},
	}))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/automations/f1", bytes.NewBufferString(aiAgentAutomationBody))
	r.Header.Set("Mattermost-User-ID", "creator1")
	router.ServeHTTP(w, r)

	require.Equal(t, http.StatusBadGateway, w.Code)
	assert.Contains(t, w.Body.String(), `failed to list tools for agent \"bot1\"`)
	require.Len(t, bridge.calls, 1)
	assert.Equal(t, "creator1", bridge.calls[0].userID, "PUT must use existing.CreatedBy for the bridge check")
}

// TestAPI_CreateAutomation_AIPrompt_BotCreatorRejected ensures bot accounts
// cannot own ai_prompt automations at create time (fail closed before bridge).
func TestAPI_CreateAutomation_AIPrompt_BotCreatorRejected(t *testing.T) {
	bridge := &stubAgentToolsLister{tools: []bridgeclient.BridgeToolInfo{}}
	api := &plugintest.API{}
	expectLogCalls(api)
	api.On("HasPermissionTo", "bot1", mmmodel.PermissionManageSystem).Return(true)
	api.On("GetUser", "bot1").Return(&mmmodel.User{Id: "bot1", IsBot: true}, (*mmmodel.AppError)(nil))

	router, _ := setupAPIWithBridge(t, api, bridge)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/automations", bytes.NewBufferString(aiAgentAutomationBody))
	r.Header.Set("Mattermost-User-ID", "bot1")
	router.ServeHTTP(w, r)

	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "bot accounts cannot create or own automations with ai_prompt")
	assert.Empty(t, bridge.calls, "bridge must not be called when the creator is a bot")
}

// errStore wraps an underlying model.Store and forces SaveWithChannelLimit
// to return a configured error so handler-level error mapping can be tested
// without driving the real KV store into a failure mode.
type errStore struct {
	model.Store
	saveErr error
}

func (e *errStore) SaveWithChannelLimit(_ *model.Automation, _ int, _ string) error {
	return e.saveErr
}

func setupAPIWithStore(t *testing.T, store model.Store, limit int) *mux.Router {
	t.Helper()

	api := &plugintest.API{}
	expectLogCalls(api)
	api.On("HasPermissionTo", mock.Anything, mmmodel.PermissionManageSystem).Return(false).Maybe()
	api.On("GetChannel", mock.Anything).Return(
		&mmmodel.Channel{Type: mmmodel.ChannelTypeOpen}, nil,
	).Maybe()
	api.On("GetChannelMember", mock.Anything, mock.Anything).Return(&mmmodel.ChannelMember{}, nil).Maybe()
	api.On("HasPermissionToChannel", mock.Anything, mock.Anything, mmmodel.PermissionManageChannelRoles).Return(true).Maybe()

	handler := NewAPIHandler(store, nil, api, newTestRegistry(), nil, &testConfig{maxAutomationsPerChannel: limit}, nil)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)
	return router
}

func TestAPI_CreateAutomation_BackendErrorReturns500(t *testing.T) {
	underlying, _ := setupStore(t)
	store := &errStore{Store: underlying, saveErr: errors.New("boom: backend down")}
	router := setupAPIWithStore(t, store, 5)

	body := `{
		"name": "Backend Error Automation",
		"enabled": true,
		"trigger": {"message_posted": {"channel_id": "ch1"}},
		"actions": [{"id": "send-msg", "send_message": {"channel_id": "ch1", "body": "hi"}}]
	}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/automations", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Contains(t, w.Body.String(), "failed to create automation")
}

func TestAPI_CreateAutomation_QuotaSentinelReturns409(t *testing.T) {
	underlying, _ := setupStore(t)
	store := &errStore{Store: underlying, saveErr: model.ErrChannelAutomationLimitExceeded}
	router := setupAPIWithStore(t, store, 5)

	body := `{
		"name": "Quota Automation",
		"enabled": true,
		"trigger": {"message_posted": {"channel_id": "ch1"}},
		"actions": [{"id": "send-msg", "send_message": {"channel_id": "ch1", "body": "hi"}}]
	}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/automations", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Contains(t, w.Body.String(), "maximum")
}

func TestAPI_UpdateAutomation_BackendErrorReturns500(t *testing.T) {
	underlying, _ := setupStore(t)
	// Seed an existing automation via the underlying store so the handler
	// can load it before invoking SaveWithChannelLimit.
	require.NoError(t, underlying.Save(&model.Automation{
		ID:        "a1",
		Name:      "Original",
		CreatedBy: "user1",
		Trigger:   model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch1"}},
	}))

	store := &errStore{Store: underlying, saveErr: errors.New("boom: backend down")}
	router := setupAPIWithStore(t, store, 5)

	body := `{
		"name": "Updated",
		"enabled": true,
		"trigger": {"message_posted": {"channel_id": "ch1"}},
		"actions": [{"id": "send-msg", "send_message": {"channel_id": "ch1", "body": "hi"}}]
	}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/automations/a1", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Contains(t, w.Body.String(), "failed to update automation")
}

func TestAPI_UpdateAutomation_QuotaSentinelReturns409(t *testing.T) {
	underlying, _ := setupStore(t)
	require.NoError(t, underlying.Save(&model.Automation{
		ID:        "a1",
		Name:      "Original",
		CreatedBy: "user1",
		Trigger:   model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch1"}},
	}))

	store := &errStore{Store: underlying, saveErr: model.ErrChannelAutomationLimitExceeded}
	router := setupAPIWithStore(t, store, 5)

	body := `{
		"name": "Updated",
		"enabled": true,
		"trigger": {"message_posted": {"channel_id": "ch1"}},
		"actions": [{"id": "send-msg", "send_message": {"channel_id": "ch1", "body": "hi"}}]
	}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/automations/a1", bytes.NewBufferString(body))
	r.Header.Set("Mattermost-User-ID", "user1")

	router.ServeHTTP(w, r)
	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Contains(t, w.Body.String(), "maximum")
}
