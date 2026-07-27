package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/mattermost/mattermost-plugin-agents/public/bridgeclient"
	mmmodel "github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin/plugintest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/mattermost/mattermost-plugin-channel-automation/server/automation"
	"github.com/mattermost/mattermost-plugin-channel-automation/server/automation/trigger"
	"github.com/mattermost/mattermost-plugin-channel-automation/server/model"
	"github.com/mattermost/mattermost-plugin-channel-automation/server/workqueue"
)

func TestOnActivate_LicenseGate(t *testing.T) {
	noDevConfig := &mmmodel.Config{
		ServiceSettings: mmmodel.ServiceSettings{
			EnableTesting:   new(bool),
			EnableDeveloper: new(bool),
		},
	}
	devConfig := &mmmodel.Config{
		ServiceSettings: mmmodel.ServiceSettings{
			EnableTesting:   new(true),
			EnableDeveloper: new(true),
		},
	}
	enterpriseLicense := &mmmodel.License{Features: &mmmodel.Features{}}

	tests := []struct {
		name           string
		config         *mmmodel.Config
		license        *mmmodel.License
		wantLicenseErr bool
	}{
		{
			name:           "rejects without license or dev mode",
			config:         noDevConfig,
			license:        nil,
			wantLicenseErr: true,
		},
		{
			name:           "allows with enterprise license",
			config:         noDevConfig,
			license:        enterpriseLicense,
			wantLicenseErr: false,
		},
		{
			name:           "allows in development mode",
			config:         devConfig,
			license:        nil,
			wantLicenseErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			api := &plugintest.API{}
			api.On("GetConfig").Return(tc.config)
			api.On("GetLicense").Return(tc.license)
			if !tc.wantLicenseErr {
				// Mock the minimum to reach past the license gate.
				// EnsureBot calls GetServerVersion before acquiring any mutex;
				// returning a version below the minimum makes it fail immediately
				// without touching KV storage, giving us a clean non-license error.
				api.On("GetServerVersion").Return("4.0.0")
			}

			p := &Plugin{}
			p.SetAPI(api)

			err := p.OnActivate()
			require.Error(t, err)
			if tc.wantLicenseErr {
				assert.Contains(t, err.Error(), "Enterprise license")
			} else {
				assert.NotContains(t, err.Error(), "Enterprise license")
			}
		})
	}
}

func TestServeHTTP(t *testing.T) {
	t.Run("unauthenticated request returns 401", func(t *testing.T) {
		api := &plugintest.API{}
		api.On("LogWarn", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Maybe()

		plugin := Plugin{}
		plugin.SetAPI(api)
		router := mux.NewRouter()
		router.Use(plugin.MattermostAuthorizationRequired)
		router.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		plugin.router = router

		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/test", nil)

		plugin.ServeHTTP(nil, w, r)

		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("unknown route returns 404", func(t *testing.T) {
		plugin := Plugin{}
		plugin.router = mux.NewRouter()

		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/nonexistent", nil)

		plugin.ServeHTTP(nil, w, r)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

func TestHandleGetAgentTools(t *testing.T) {
	// Use valid 26-char Mattermost IDs to pass bridge client validation.
	agentID := mmmodel.NewId()
	userID := mmmodel.NewId()

	t.Run("nil bridge client returns 503", func(t *testing.T) {
		api := &plugintest.API{}
		api.On("LogWarn", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Maybe()

		p := &Plugin{}
		p.SetAPI(api)

		router := mux.NewRouter()
		router.HandleFunc("/api/v1/agents/{agent_id}/tools", p.handleGetAgentTools).Methods(http.MethodGet)

		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/agents/"+agentID+"/tools", nil)
		r.Header.Set("Mattermost-User-ID", userID)

		router.ServeHTTP(w, r)

		assert.Equal(t, http.StatusServiceUnavailable, w.Code)
		assert.Contains(t, w.Body.String(), "AI plugin bridge not available")
	})

	t.Run("successful proxy returns tools JSON", func(t *testing.T) {
		toolsResp := bridgeclient.AgentToolsResponse{
			Tools: []bridgeclient.BridgeToolInfo{
				{Name: "search", Description: "Search things"},
			},
		}
		respBody, _ := json.Marshal(toolsResp)

		api := &plugintest.API{}
		api.On("PluginHTTP", mock.MatchedBy(func(req *http.Request) bool {
			return strings.Contains(req.URL.Path, "/bridge/v1/agents/")
		})).Return(&http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(string(respBody))),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
		})

		p := &Plugin{}
		p.SetAPI(api)
		p.bridgeClient = bridgeclient.NewClient(api)

		router := mux.NewRouter()
		router.HandleFunc("/api/v1/agents/{agent_id}/tools", p.handleGetAgentTools).Methods(http.MethodGet)

		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/agents/"+agentID+"/tools", nil)
		r.Header.Set("Mattermost-User-ID", userID)

		router.ServeHTTP(w, r)

		require.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
		assert.Contains(t, w.Body.String(), `"name":"search"`)
	})

	t.Run("bridge client error returns 502", func(t *testing.T) {
		api := &plugintest.API{}
		api.On("LogError", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Maybe()
		api.On("LogError", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Maybe()
		api.On("PluginHTTP", mock.Anything).Return(&http.Response{
			StatusCode: http.StatusUnauthorized,
			Body:       io.NopCloser(strings.NewReader(`{"error":"unauthorized"}`)),
		})

		p := &Plugin{}
		p.SetAPI(api)
		p.bridgeClient = bridgeclient.NewClient(api)

		router := mux.NewRouter()
		router.HandleFunc("/api/v1/agents/{agent_id}/tools", p.handleGetAgentTools).Methods(http.MethodGet)

		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/agents/"+agentID+"/tools", nil)
		r.Header.Set("Mattermost-User-ID", userID)

		router.ServeHTTP(w, r)

		assert.Equal(t, http.StatusBadGateway, w.Code)
	})
}

func TestHandleGetClientConfig(t *testing.T) {
	t.Run("unauthenticated request returns 401 via real router", func(t *testing.T) {
		api := &plugintest.API{}
		api.On("LogWarn", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Maybe()
		p := &Plugin{}
		p.SetAPI(api)
		p.setConfiguration(&configuration{EnableUI: true})
		p.router = p.initRouter()

		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/config", nil)
		// deliberately omit Mattermost-User-ID header

		p.ServeHTTP(nil, w, r)

		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("authenticated request succeeds via real router", func(t *testing.T) {
		api := &plugintest.API{}
		p := &Plugin{}
		p.SetAPI(api)
		p.setConfiguration(&configuration{EnableUI: true})
		p.router = p.initRouter()

		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/config", nil)
		r.Header.Set("Mattermost-User-ID", mmmodel.NewId())

		p.ServeHTTP(nil, w, r)

		assert.Equal(t, http.StatusOK, w.Code)

		var resp map[string]any
		err := json.Unmarshal(w.Body.Bytes(), &resp)
		require.NoError(t, err)
		assert.Equal(t, true, resp["enable_ui"])
	})

	t.Run("returns enable_ui false by default", func(t *testing.T) {
		p := &Plugin{}
		p.setConfiguration(&configuration{})

		router := mux.NewRouter()
		router.HandleFunc("/api/v1/config", p.handleGetClientConfig).Methods(http.MethodGet)

		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/config", nil)
		r.Header.Set("Mattermost-User-ID", mmmodel.NewId())

		router.ServeHTTP(w, r)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

		var resp map[string]any
		err := json.Unmarshal(w.Body.Bytes(), &resp)
		require.NoError(t, err)
		assert.Equal(t, false, resp["enable_ui"])
	})

	t.Run("returns enable_ui true when configured", func(t *testing.T) {
		p := &Plugin{}
		p.setConfiguration(&configuration{EnableUI: true})

		router := mux.NewRouter()
		router.HandleFunc("/api/v1/config", p.handleGetClientConfig).Methods(http.MethodGet)

		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/config", nil)
		r.Header.Set("Mattermost-User-ID", mmmodel.NewId())

		router.ServeHTTP(w, r)

		assert.Equal(t, http.StatusOK, w.Code)

		var resp map[string]any
		err := json.Unmarshal(w.Body.Bytes(), &resp)
		require.NoError(t, err)
		assert.Equal(t, true, resp["enable_ui"])
	})
}

func TestMessageHasBeenPosted_SkipsAIGeneratedPosts(t *testing.T) {
	// Plugin has nil triggerService — if the early return doesn't fire,
	// we'll get a nil-pointer panic, proving the filter works.
	p := &Plugin{botUserID: "bot-id"}

	post := &mmmodel.Post{
		UserId:  "human-user",
		Message: "AI-generated reply",
	}
	post.AddProp("ai_generated_by", "some-bot-id")

	// Should return immediately without touching triggerService.
	assert.NotPanics(t, func() {
		p.MessageHasBeenPosted(nil, post)
	})
}

func TestMessageHasBeenPosted_SkipsBotPosts(t *testing.T) {
	p := &Plugin{botUserID: "bot-id"}

	post := &mmmodel.Post{UserId: "bot-id", Message: "hi"}

	assert.NotPanics(t, func() {
		p.MessageHasBeenPosted(nil, post)
	})
}

func TestMessageHasBeenPosted_SkipsSystemMessages(t *testing.T) {
	p := &Plugin{botUserID: "bot-id"}

	post := &mmmodel.Post{UserId: "human-user", Type: mmmodel.PostTypeJoinChannel}

	assert.NotPanics(t, func() {
		p.MessageHasBeenPosted(nil, post)
	})
}

func TestMessageHasBeenPosted_SkipsWebhookPosts(t *testing.T) {
	p := &Plugin{botUserID: "bot-id"}

	post := &mmmodel.Post{UserId: "human-user", Message: "from webhook"}
	post.AddProp("from_webhook", "true")

	assert.NotPanics(t, func() {
		p.MessageHasBeenPosted(nil, post)
	})
}

func TestMessageHasBeenPosted_SkipsFromBotPosts(t *testing.T) {
	p := &Plugin{botUserID: "bot-id"}

	post := &mmmodel.Post{UserId: "human-user", Message: "from bot"}
	post.AddProp("from_bot", "true")

	assert.NotPanics(t, func() {
		p.MessageHasBeenPosted(nil, post)
	})
}

func TestMessageHasBeenPosted_SkipsIsBotUser(t *testing.T) {
	// Bot account without the from_bot prop — identity check must still drop it.
	api := &plugintest.API{}
	api.On("GetUser", "bot-user-id").Return(&mmmodel.User{Id: "bot-user-id", IsBot: true}, nil)
	api.On("LogError", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return().Maybe()

	p := &Plugin{botUserID: "plugin-bot-id"}
	p.SetAPI(api)

	post := &mmmodel.Post{UserId: "bot-user-id", ChannelId: "ch1", Message: "hi"}
	assert.NotPanics(t, func() {
		p.MessageHasBeenPosted(nil, post)
	})
	api.AssertCalled(t, "GetUser", "bot-user-id")
}

func TestChannelHasBeenCreated_SkipsNonPublicChannels(t *testing.T) {
	p := &Plugin{botUserID: "bot-id"}

	ch := &mmmodel.Channel{Id: "ch1", Type: mmmodel.ChannelTypePrivate}

	assert.NotPanics(t, func() {
		p.ChannelHasBeenCreated(nil, ch)
	})
}

func TestHandleMembershipChange_SkipsBotUser(t *testing.T) {
	p := &Plugin{botUserID: "bot-id"}

	member := &mmmodel.ChannelMember{UserId: "bot-id", ChannelId: "ch1"}

	assert.NotPanics(t, func() {
		p.handleMembershipChange(member, "joined")
	})
}

func TestHandleMembershipChange_SkipsIsBotUser(t *testing.T) {
	api := &plugintest.API{}
	api.On("GetUser", "bot-user-id").Return(&mmmodel.User{Id: "bot-user-id", IsBot: true}, nil)

	p := &Plugin{botUserID: "other-bot"}
	p.SetAPI(api)

	member := &mmmodel.ChannelMember{UserId: "bot-user-id", ChannelId: "ch1"}

	assert.NotPanics(t, func() {
		p.handleMembershipChange(member, "joined")
	})
}

// inMemoryKV is a thread-safe in-memory KV store for plugin hook tests.
type inMemoryKV struct {
	mu   sync.Mutex
	data map[string][]byte
}

// setupPluginForHookTest creates a Plugin with real stores/registry
// suitable for testing hook happy paths end-to-end.
func setupPluginForHookTest(t *testing.T, triggerType string) (*Plugin, *workqueue.Store) {
	t.Helper()

	kv := &inMemoryKV{data: make(map[string][]byte)}

	api := &plugintest.API{}
	api.On("KVGet", mock.Anything).Return(
		func(key string) []byte {
			kv.mu.Lock()
			defer kv.mu.Unlock()
			d := kv.data[key]
			if d == nil {
				return nil
			}
			cp := make([]byte, len(d))
			copy(cp, d)
			return cp
		},
		func(_ string) *mmmodel.AppError { return nil },
	)
	api.On("KVSet", mock.Anything, mock.Anything).Return(
		func(key string, value []byte) *mmmodel.AppError {
			kv.mu.Lock()
			defer kv.mu.Unlock()
			cp := make([]byte, len(value))
			copy(cp, value)
			kv.data[key] = cp
			return nil
		},
	)
	api.On("KVDelete", mock.Anything).Return(
		func(key string) *mmmodel.AppError {
			kv.mu.Lock()
			defer kv.mu.Unlock()
			delete(kv.data, key)
			return nil
		},
	)
	// Register LogDebug/LogError for varying argument counts across hooks.
	for _, method := range []string{"LogDebug", "LogError", "LogWarn"} {
		for _, n := range []int{3, 5, 7, 9, 11, 13, 15} {
			args := make([]any, n)
			for i := range args {
				args[i] = mock.Anything
			}
			api.On(method, args...).Return().Maybe()
		}
	}
	api.On("GetChannel", mock.Anything).Return(&mmmodel.Channel{Id: "ch1", Name: "test", DisplayName: "Test"}, nil)
	api.On("GetUser", mock.Anything).Return(&mmmodel.User{Id: "user1", Username: "testuser"}, nil)
	api.On("GetTeam", mock.Anything).Return(&mmmodel.Team{Id: "team1", Name: "testteam", DisplayName: "Test Team"}, nil).Maybe()
	api.On("GetChannelByName", mock.Anything, mmmodel.DefaultChannelName, false).Return(&mmmodel.Channel{Id: "town-square-id", Name: mmmodel.DefaultChannelName}, nil).Maybe()

	registry := automation.NewRegistry()
	switch triggerType {
	case model.TriggerTypeMessagePosted:
		registry.RegisterTrigger(&trigger.MessagePostedTrigger{})
	case model.TriggerTypeChannelCreated:
		registry.RegisterTrigger(&trigger.ChannelCreatedTrigger{})
	case model.TriggerTypeMembershipChanged:
		registry.RegisterTrigger(&trigger.MembershipChangedTrigger{})
	case model.TriggerTypeUserJoinedTeam:
		registry.RegisterTrigger(&trigger.UserJoinedTeamTrigger{})
	}

	automationStore := automation.NewStore(api, &sync.Mutex{})
	triggerService := automation.NewTriggerService(automationStore, registry)
	wqStore := workqueue.NewStore(api, &sync.Mutex{})

	// Create a WorkerPool but don't start it — we just need Notify() to not block.
	executor := automation.NewExecutor(registry)
	wp := workqueue.NewWorkerPool(wqStore, executor, automationStore, nil, nil, api, 1)

	p := &Plugin{
		botUserID:       "bot-id",
		registry:        registry,
		automationStore: automationStore,
		triggerService:  triggerService,
		workQueueStore:  wqStore,
		workerPool:      wp,
	}
	p.SetAPI(api)
	p.dispatcher = automation.NewDispatcher(api, triggerService, wqStore, wp)

	return p, wqStore
}

func TestMessageHasBeenPosted_ProcessesNormalPost(t *testing.T) {
	p, wqStore := setupPluginForHookTest(t, model.TriggerTypeMessagePosted)

	// Save an automation triggered by messages in ch1.
	f := &model.Automation{
		ID:      "f1",
		Name:    "Test Automation",
		Enabled: true,
		Trigger: model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch1"}},
		Actions: []model.Action{{ID: "a1", SendMessage: &model.SendMessageActionConfig{ChannelID: "ch1", Body: "hello"}}},
	}
	require.NoError(t, p.automationStore.Save(f))

	post := &mmmodel.Post{Id: "post1", UserId: "user1", ChannelId: "ch1", Message: "hello"}
	p.MessageHasBeenPosted(nil, post)

	// Verify a work item was enqueued.
	require.Eventually(t, func() bool {
		item, _ := wqStore.ClaimNext()
		return item != nil
	}, 2*time.Second, 10*time.Millisecond)
}

func TestChannelHasBeenCreated_ProcessesPublicChannel(t *testing.T) {
	p, wqStore := setupPluginForHookTest(t, model.TriggerTypeChannelCreated)

	f := &model.Automation{
		ID:      "f1",
		Name:    "Test Automation",
		Enabled: true,
		Trigger: model.Trigger{ChannelCreated: &model.ChannelCreatedConfig{TeamID: "team1"}},
		Actions: []model.Action{{ID: "a1", SendMessage: &model.SendMessageActionConfig{ChannelID: "ch1", Body: "welcome"}}},
	}
	require.NoError(t, p.automationStore.Save(f))

	ch := &mmmodel.Channel{Id: "ch-new", Name: "new-channel", DisplayName: "New Channel", Type: mmmodel.ChannelTypeOpen, TeamId: "team1", CreatorId: "user1"}
	p.ChannelHasBeenCreated(nil, ch)

	require.Eventually(t, func() bool {
		item, _ := wqStore.ClaimNext()
		return item != nil
	}, 2*time.Second, 10*time.Millisecond)
}

func TestHandleMembershipChange_ProcessesNormalUser(t *testing.T) {
	p, wqStore := setupPluginForHookTest(t, model.TriggerTypeMembershipChanged)

	f := &model.Automation{
		ID:      "f1",
		Name:    "Test Automation",
		Enabled: true,
		Trigger: model.Trigger{MembershipChanged: &model.MembershipChangedConfig{ChannelID: "ch1"}},
		Actions: []model.Action{{ID: "a1", SendMessage: &model.SendMessageActionConfig{ChannelID: "ch1", Body: "welcome"}}},
	}
	require.NoError(t, p.automationStore.Save(f))

	member := &mmmodel.ChannelMember{UserId: "user1", ChannelId: "ch1"}
	p.handleMembershipChange(member, "joined")

	require.Eventually(t, func() bool {
		item, _ := wqStore.ClaimNext()
		return item != nil
	}, 2*time.Second, 10*time.Millisecond)
}

func TestUserHasJoinedTeam_SkipsBotUser(t *testing.T) {
	// The botUserID short-circuit fires before GetUser, so no API mock needed.
	p := &Plugin{botUserID: "bot-id"}

	member := &mmmodel.TeamMember{UserId: "bot-id", TeamId: "team1"}

	assert.NotPanics(t, func() {
		p.UserHasJoinedTeam(nil, member, nil)
	})
}

func TestUserHasJoinedTeam_SkipsIsBotUser(t *testing.T) {
	api := &plugintest.API{}
	api.On("GetUser", "bot-user-id").Return(&mmmodel.User{Id: "bot-user-id", IsBot: true}, nil)

	p := &Plugin{botUserID: "other-bot"}
	p.SetAPI(api)

	member := &mmmodel.TeamMember{UserId: "bot-user-id", TeamId: "team1"}

	assert.NotPanics(t, func() {
		p.UserHasJoinedTeam(nil, member, nil)
	})
}

func TestUserHasJoinedTeam_ProcessesNormalUser(t *testing.T) {
	p, wqStore := setupPluginForHookTest(t, model.TriggerTypeUserJoinedTeam)

	f := &model.Automation{
		ID:      "f1",
		Name:    "Team Join Automation",
		Enabled: true,
		Trigger: model.Trigger{UserJoinedTeam: &model.UserJoinedTeamConfig{TeamID: "team1"}},
		Actions: []model.Action{{ID: "a1", SendMessage: &model.SendMessageActionConfig{ChannelID: "ch1", Body: "welcome"}}},
	}
	require.NoError(t, p.automationStore.Save(f))

	member := &mmmodel.TeamMember{UserId: "user1", TeamId: "team1"}
	p.UserHasJoinedTeam(nil, member, nil)

	require.Eventually(t, func() bool {
		item, _ := wqStore.ClaimNext()
		return item != nil
	}, 2*time.Second, 10*time.Millisecond)
}

func TestUserHasJoinedTeam_UsesPlaceholdersWhenTeamLookupFails(t *testing.T) {
	kv := &inMemoryKV{data: make(map[string][]byte)}

	api := &plugintest.API{}
	api.On("KVGet", mock.Anything).Return(
		func(key string) []byte {
			kv.mu.Lock()
			defer kv.mu.Unlock()
			d := kv.data[key]
			if d == nil {
				return nil
			}
			cp := make([]byte, len(d))
			copy(cp, d)
			return cp
		},
		func(_ string) *mmmodel.AppError { return nil },
	)
	api.On("KVSet", mock.Anything, mock.Anything).Return(
		func(key string, value []byte) *mmmodel.AppError {
			kv.mu.Lock()
			defer kv.mu.Unlock()
			cp := make([]byte, len(value))
			copy(cp, value)
			kv.data[key] = cp
			return nil
		},
	)
	api.On("KVDelete", mock.Anything).Return(
		func(key string) *mmmodel.AppError {
			kv.mu.Lock()
			defer kv.mu.Unlock()
			delete(kv.data, key)
			return nil
		},
	)
	for _, method := range []string{"LogDebug", "LogError", "LogWarn"} {
		for _, n := range []int{3, 5, 7, 9, 11, 13, 15} {
			args := make([]any, n)
			for i := range args {
				args[i] = mock.Anything
			}
			api.On(method, args...).Return().Maybe()
		}
	}
	api.On("GetUser", mock.Anything).Return(&mmmodel.User{Id: "user1", Username: "testuser"}, nil)
	api.On("GetTeam", mock.Anything).Return(nil, &mmmodel.AppError{Message: "team lookup failed"})
	api.On("GetChannelByName", mock.Anything, mmmodel.DefaultChannelName, false).Return(nil, &mmmodel.AppError{Message: "channel lookup failed"})

	registry := automation.NewRegistry()
	registry.RegisterTrigger(&trigger.UserJoinedTeamTrigger{})

	automationStore := automation.NewStore(api, &sync.Mutex{})
	triggerService := automation.NewTriggerService(automationStore, registry)
	wqStore := workqueue.NewStore(api, &sync.Mutex{})
	executor := automation.NewExecutor(registry)
	wp := workqueue.NewWorkerPool(wqStore, executor, automationStore, nil, nil, api, 1)

	p := &Plugin{
		botUserID:       "bot-id",
		registry:        registry,
		automationStore: automationStore,
		triggerService:  triggerService,
		workQueueStore:  wqStore,
		workerPool:      wp,
	}
	p.SetAPI(api)
	p.dispatcher = automation.NewDispatcher(api, triggerService, wqStore, wp)

	f := &model.Automation{
		ID:      "f1",
		Name:    "Team Join Automation",
		Enabled: true,
		Trigger: model.Trigger{UserJoinedTeam: &model.UserJoinedTeamConfig{TeamID: "team1"}},
		Actions: []model.Action{{ID: "a1", SendMessage: &model.SendMessageActionConfig{ChannelID: "{{.Trigger.Team.DefaultChannelId}}", Body: "welcome"}}},
	}
	require.NoError(t, automationStore.Save(f))

	member := &mmmodel.TeamMember{UserId: "user1", TeamId: "team1"}
	p.UserHasJoinedTeam(nil, member, nil)

	var item *model.WorkItem
	require.Eventually(t, func() bool {
		it, _ := wqStore.ClaimNext()
		if it != nil {
			item = it
			return true
		}
		return false
	}, 2*time.Second, 10*time.Millisecond)

	require.NotNil(t, item.TriggerData.Team)
	assert.Equal(t, "team1", item.TriggerData.Team.Id, "Team.Id should fall back to event.Team.Id when GetTeam fails")
	assert.Empty(t, item.TriggerData.Team.Name)
	assert.Empty(t, item.TriggerData.Team.DisplayName)
	assert.Empty(t, item.TriggerData.Team.DefaultChannelId, "DefaultChannelId should stay empty on failure so SendMessage fails fast")
}
