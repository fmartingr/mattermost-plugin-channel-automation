package workqueue

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mmmodel "github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin/plugintest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/mattermost/mattermost-plugin-channel-automation/server/automation"
	"github.com/mattermost/mattermost-plugin-channel-automation/server/automation/notifier"
	"github.com/mattermost/mattermost-plugin-channel-automation/server/model"
)

// testAction is a controllable action handler for testing.
type testAction struct {
	mu        sync.Mutex
	execCount int
	execFn    func() error
	running   atomic.Int32 // concurrency tracking
	maxSeen   atomic.Int32
}

func (a *testAction) Type() string { return "send_message" }

func (a *testAction) Execute(_ *model.Action, _ *model.AutomationContext) (*model.StepOutput, error) {
	cur := a.running.Add(1)
	defer a.running.Add(-1)

	// Track max concurrent
	for {
		old := a.maxSeen.Load()
		if cur <= old || a.maxSeen.CompareAndSwap(old, cur) {
			break
		}
	}

	a.mu.Lock()
	a.execCount++
	fn := a.execFn
	a.mu.Unlock()

	if fn != nil {
		if err := fn(); err != nil {
			return nil, err
		}
	}

	return &model.StepOutput{Message: "done"}, nil
}

func (a *testAction) getExecCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.execCount
}

// testAutomationStore is a simple in-memory implementation of model.Store.
type testAutomationStore struct {
	mu          sync.Mutex
	automations map[string]*model.Automation
}

func newTestAutomationStore() *testAutomationStore {
	return &testAutomationStore{automations: make(map[string]*model.Automation)}
}

func (s *testAutomationStore) Get(id string) (*model.Automation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.automations[id]
	return f, nil
}

func (s *testAutomationStore) List() ([]*model.Automation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]*model.Automation, 0, len(s.automations))
	for _, f := range s.automations {
		result = append(result, f)
	}
	return result, nil
}

func (s *testAutomationStore) Save(f *model.Automation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.automations[f.ID] = f
	return nil
}

func (s *testAutomationStore) SaveWithChannelLimit(a *model.Automation, _ int, _ string) error {
	return s.Save(a)
}

func (s *testAutomationStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.automations, id)
	return nil
}

func (s *testAutomationStore) CountByTriggerChannel(_ string) (int, error) {
	return 0, nil
}

func (s *testAutomationStore) ListByTriggerChannel(_ string) ([]*model.Automation, error) {
	return nil, nil
}

func (s *testAutomationStore) ListScheduled() ([]*model.Automation, error) {
	return nil, nil
}

func (s *testAutomationStore) GetAutomationIDsForChannel(_ string) ([]string, error) {
	return nil, nil
}

func (s *testAutomationStore) GetAutomationIDsForMembershipChannel(_ string) ([]string, error) {
	return nil, nil
}

func (s *testAutomationStore) GetChannelCreatedAutomationIDs() ([]string, error) {
	return nil, nil
}

func (s *testAutomationStore) GetAutomationIDsForUserJoinedTeam(_ string) ([]string, error) {
	return nil, nil
}

func setupWorkerPool(t *testing.T, maxWorkers int, act *testAction) (*WorkerPool, *Store, *testAutomationStore) {
	t.Helper()

	store, _ := setupStore(t)

	api := &plugintest.API{}
	api.On("LogInfo", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	api.On("LogWarn", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	api.On("LogError", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	api.On("GetUser", mock.Anything).Return(&mmmodel.User{DeleteAt: 0}, nil)
	api.On("HasPermissionTo", mock.Anything, mock.Anything).Return(true)

	registry := automation.NewRegistry()
	registry.RegisterAction(act)
	executor := automation.NewExecutor(registry)

	automationStore := newTestAutomationStore()

	wp := NewWorkerPool(store, executor, automationStore, nil, nil, api, maxWorkers)
	wp.pollInterval = 50 * time.Millisecond // speed up tests

	return wp, store, automationStore
}

func TestWorkerPool_ProcessesItems(t *testing.T) {
	act := &testAction{}
	wp, store, automationStore := setupWorkerPool(t, 4, act)

	_ = automationStore.Save(&model.Automation{ID: "f1", Name: "Automation 1", Enabled: true, Actions: []model.Action{{ID: "a1", SendMessage: &model.SendMessageActionConfig{}}}})

	for i := range 3 {
		item := &model.WorkItem{
			ID:             fmt.Sprintf("w%d", i),
			AutomationID:   "f1",
			AutomationName: "Automation 1",
		}
		require.NoError(t, store.Enqueue(item))
	}

	wp.Start()
	wp.Notify()

	// Wait for items to be processed.
	require.Eventually(t, func() bool {
		return act.getExecCount() == 3
	}, 5*time.Second, 10*time.Millisecond)

	wp.Stop()

	// All items should be completed (deleted from KV).
	for i := range 3 {
		got, err := store.Get(fmt.Sprintf("w%d", i))
		require.NoError(t, err)
		assert.Nil(t, got)
	}
}

func TestWorkerPool_ConcurrencyLimit(t *testing.T) {
	// Use a blocking action to verify concurrency limits.
	proceed := make(chan struct{})
	act := &testAction{
		execFn: func() error {
			<-proceed
			return nil
		},
	}

	wp, store, automationStore := setupWorkerPool(t, 2, act)

	_ = automationStore.Save(&model.Automation{ID: "f1", Name: "Automation 1", Enabled: true, Actions: []model.Action{{ID: "a1", SendMessage: &model.SendMessageActionConfig{}}}})

	for i := range 5 {
		item := &model.WorkItem{
			ID:             fmt.Sprintf("w%d", i),
			AutomationID:   "f1",
			AutomationName: "Automation 1",
		}
		require.NoError(t, store.Enqueue(item))
	}

	wp.Start()
	wp.Notify()

	// Wait until 2 workers are running.
	require.Eventually(t, func() bool {
		return act.running.Load() == 2
	}, 5*time.Second, 10*time.Millisecond)

	// Max concurrent should be exactly 2.
	assert.Equal(t, int32(2), act.maxSeen.Load())

	// Release all workers.
	close(proceed)

	require.Eventually(t, func() bool {
		return act.getExecCount() == 5
	}, 5*time.Second, 10*time.Millisecond)

	wp.Stop()
}

func TestWorkerPool_GracefulShutdown(t *testing.T) {
	started := make(chan struct{})
	proceed := make(chan struct{})
	act := &testAction{
		execFn: func() error {
			select {
			case started <- struct{}{}:
			default:
			}
			<-proceed
			return nil
		},
	}

	wp, store, automationStore := setupWorkerPool(t, 4, act)

	_ = automationStore.Save(&model.Automation{ID: "f1", Name: "Automation 1", Enabled: true, Actions: []model.Action{{ID: "a1", SendMessage: &model.SendMessageActionConfig{}}}})

	item := &model.WorkItem{ID: "w1", AutomationID: "f1", AutomationName: "Automation 1"}
	require.NoError(t, store.Enqueue(item))

	wp.Start()
	wp.Notify()

	// Wait for the worker to start.
	<-started

	// Initiate shutdown in a goroutine.
	done := make(chan struct{})
	go func() {
		wp.Stop()
		close(done)
	}()

	// Stop should not return yet because worker is still running.
	select {
	case <-done:
		t.Fatal("Stop returned before worker finished")
	case <-time.After(100 * time.Millisecond):
	}

	// Let the worker finish.
	close(proceed)

	// Now Stop should return.
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return after worker finished")
	}
}

func TestWorkerPool_NotifyWakesDispatcher(t *testing.T) {
	act := &testAction{}
	wp, store, automationStore := setupWorkerPool(t, 4, act)
	wp.pollInterval = 10 * time.Minute // very long poll interval

	_ = automationStore.Save(&model.Automation{ID: "f1", Name: "Automation 1", Enabled: true, Actions: []model.Action{{ID: "a1", SendMessage: &model.SendMessageActionConfig{}}}})

	wp.Start()
	defer wp.Stop()

	item := &model.WorkItem{ID: "w1", AutomationID: "f1", AutomationName: "Automation 1"}
	require.NoError(t, store.Enqueue(item))

	// Without Notify, the dispatcher won't process until the long poll interval.
	wp.Notify()

	require.Eventually(t, func() bool {
		return act.getExecCount() == 1
	}, 5*time.Second, 10*time.Millisecond)
}

func TestWorkerPool_FailedExecution(t *testing.T) {
	act := &testAction{
		execFn: func() error {
			return fmt.Errorf("action failed")
		},
	}

	wp, store, automationStore := setupWorkerPool(t, 4, act)

	_ = automationStore.Save(&model.Automation{ID: "f1", Name: "Automation 1", Enabled: true, Actions: []model.Action{{ID: "a1", SendMessage: &model.SendMessageActionConfig{}}}})

	item := &model.WorkItem{ID: "w1", AutomationID: "f1", AutomationName: "Automation 1"}
	require.NoError(t, store.Enqueue(item))

	wp.Start()
	wp.Notify()

	require.Eventually(t, func() bool {
		return act.getExecCount() == 1
	}, 5*time.Second, 10*time.Millisecond)

	wp.Stop()

	// Item should be deleted (failure details live in the execution record).
	got, err := store.Get("w1")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestWorkerPool_DeletedAutomation(t *testing.T) {
	act := &testAction{}
	wp, store, automationStore := setupWorkerPool(t, 4, act)

	// Don't add automation to store — it's "deleted"

	item := &model.WorkItem{ID: "w1", AutomationID: "f1", AutomationName: "Automation 1"}
	require.NoError(t, store.Enqueue(item))

	wp.Start()
	wp.Notify()

	// Wait for processing.
	require.Eventually(t, func() bool {
		got, _ := store.Get("w1")
		return got == nil
	}, 5*time.Second, 10*time.Millisecond)

	wp.Stop()

	// Action should never have been called.
	assert.Equal(t, 0, act.getExecCount())

	// Item should be completed (deleted from KV) — not marked as failed.
	_ = automationStore
}

func TestWorkerPool_PanicRecovery(t *testing.T) {
	callCount := 0
	act := &testAction{
		execFn: func() error {
			callCount++
			if callCount == 1 {
				panic("boom")
			}
			return nil
		},
	}

	wp, store, automationStore := setupWorkerPool(t, 1, act)

	_ = automationStore.Save(&model.Automation{ID: "f1", Name: "Automation 1", Enabled: true, Actions: []model.Action{{ID: "a1", SendMessage: &model.SendMessageActionConfig{}}}})

	// Enqueue an item that will panic.
	item1 := &model.WorkItem{ID: "w1", AutomationID: "f1", AutomationName: "Automation 1"}
	require.NoError(t, store.Enqueue(item1))

	wp.Start()
	wp.Notify()

	// Wait for the panicking item to be deleted.
	require.Eventually(t, func() bool {
		got, _ := store.Get("w1")
		return got == nil
	}, 5*time.Second, 10*time.Millisecond)

	// Enqueue a second item that should succeed, proving the pool survived.
	item2 := &model.WorkItem{ID: "w2", AutomationID: "f1", AutomationName: "Automation 1"}
	require.NoError(t, store.Enqueue(item2))
	wp.Notify()

	require.Eventually(t, func() bool {
		got, _ := store.Get("w2")
		return got == nil // completed items are deleted
	}, 5*time.Second, 10*time.Millisecond)

	wp.Stop()

	// The action was called twice total (once panicking, once succeeding).
	assert.Equal(t, 2, act.getExecCount())
}

func TestWorkerPool_CreatorLookupError(t *testing.T) {
	act := &testAction{}

	store, _ := setupStore(t)
	api := &plugintest.API{}
	api.On("LogInfo", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	api.On("LogWarn", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	api.On("LogError", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	api.On("GetUser", "some-user").Return(nil, mmmodel.NewAppError("GetUser", "app.user.get.app_error", nil, "", 500))

	registry := automation.NewRegistry()
	registry.RegisterAction(act)
	executor := automation.NewExecutor(registry)
	automationStore := newTestAutomationStore()

	wp := NewWorkerPool(store, executor, automationStore, nil, nil, api, 4)
	wp.pollInterval = 50 * time.Millisecond

	_ = automationStore.Save(&model.Automation{ID: "f1", Name: "Automation 1", Enabled: true, CreatedBy: "some-user", Actions: []model.Action{{ID: "a1", SendMessage: &model.SendMessageActionConfig{}}}})

	item := &model.WorkItem{ID: "w1", AutomationID: "f1", AutomationName: "Automation 1"}
	require.NoError(t, store.Enqueue(item))

	wp.Start()
	wp.Notify()

	require.Eventually(t, func() bool {
		got, _ := store.Get("w1")
		return got == nil
	}, 5*time.Second, 10*time.Millisecond)

	wp.Stop()

	// Action should never have been called.
	assert.Equal(t, 0, act.getExecCount())

	// Automation should remain enabled — this is a transient error.
	f, _ := automationStore.Get("f1")
	require.NotNil(t, f)
	assert.True(t, f.Enabled)
}

func TestWorkerPool_CreatorPermanentlyDeleted(t *testing.T) {
	act := &testAction{}

	store, _ := setupStore(t)
	api := &plugintest.API{}
	api.On("LogInfo", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	api.On("LogWarn", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	api.On("LogError", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	api.On("GetUser", "deleted-user").Return(nil, mmmodel.NewAppError("GetUser", "app.user.missing.app_error", nil, "", 404))

	registry := automation.NewRegistry()
	registry.RegisterAction(act)
	executor := automation.NewExecutor(registry)
	automationStore := newTestAutomationStore()

	wp := NewWorkerPool(store, executor, automationStore, nil, nil, api, 4)
	wp.pollInterval = 50 * time.Millisecond

	_ = automationStore.Save(&model.Automation{ID: "f1", Name: "Automation 1", Enabled: true, CreatedBy: "deleted-user", Actions: []model.Action{{ID: "a1", SendMessage: &model.SendMessageActionConfig{}}}})

	item := &model.WorkItem{ID: "w1", AutomationID: "f1", AutomationName: "Automation 1"}
	require.NoError(t, store.Enqueue(item))

	wp.Start()
	wp.Notify()

	require.Eventually(t, func() bool {
		got, _ := store.Get("w1")
		return got == nil
	}, 5*time.Second, 10*time.Millisecond)

	wp.Stop()

	// Action should never have been called.
	assert.Equal(t, 0, act.getExecCount())

	// Automation should be disabled — creator is permanently gone.
	f, _ := automationStore.Get("f1")
	require.NotNil(t, f)
	assert.False(t, f.Enabled)
}

func TestWorkerPool_CreatorDeactivated(t *testing.T) {
	act := &testAction{}

	store, _ := setupStore(t)
	api := &plugintest.API{}
	api.On("LogInfo", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	api.On("LogWarn", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	api.On("LogError", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	api.On("GetUser", "deactivated-user").Return(&mmmodel.User{DeleteAt: 1234567890}, nil)

	registry := automation.NewRegistry()
	registry.RegisterAction(act)
	executor := automation.NewExecutor(registry)
	automationStore := newTestAutomationStore()

	wp := NewWorkerPool(store, executor, automationStore, nil, nil, api, 4)
	wp.pollInterval = 50 * time.Millisecond

	_ = automationStore.Save(&model.Automation{ID: "f1", Name: "Automation 1", Enabled: true, CreatedBy: "deactivated-user", Actions: []model.Action{{ID: "a1", SendMessage: &model.SendMessageActionConfig{}}}})

	item := &model.WorkItem{ID: "w1", AutomationID: "f1", AutomationName: "Automation 1"}
	require.NoError(t, store.Enqueue(item))

	wp.Start()
	wp.Notify()

	require.Eventually(t, func() bool {
		got, _ := store.Get("w1")
		return got == nil
	}, 5*time.Second, 10*time.Millisecond)

	wp.Stop()

	// Action should never have been called.
	assert.Equal(t, 0, act.getExecCount())

	// Automation should be disabled.
	f, _ := automationStore.Get("f1")
	require.NotNil(t, f)
	assert.False(t, f.Enabled)
}

func TestWorkerPool_DisableNotifiesCreator(t *testing.T) {
	act := &testAction{}

	store, _ := setupStore(t)
	api := &plugintest.API{}
	api.On("LogInfo", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	api.On("LogWarn", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	api.On("LogError", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	api.On("GetUser", "deactivated-user").Return(&mmmodel.User{DeleteAt: 1234567890}, nil)

	registry := automation.NewRegistry()
	registry.RegisterAction(act)
	executor := automation.NewExecutor(registry)
	automationStore := newTestAutomationStore()
	fakeNotifier := &fakeFailureNotifier{}

	wp := NewWorkerPool(store, executor, automationStore, nil, fakeNotifier, api, 4)
	wp.pollInterval = 50 * time.Millisecond

	_ = automationStore.Save(&model.Automation{ID: "f1", Name: "Automation 1", Enabled: true, CreatedBy: "deactivated-user", Actions: []model.Action{{ID: "a1", SendMessage: &model.SendMessageActionConfig{}}}})

	item := &model.WorkItem{ID: "w1", AutomationID: "f1", AutomationName: "Automation 1"}
	require.NoError(t, store.Enqueue(item))

	wp.Start()
	wp.Notify()

	require.Eventually(t, func() bool {
		got, _ := store.Get("w1")
		return got == nil
	}, 5*time.Second, 10*time.Millisecond)

	wp.Stop()

	// The creator must be DMed exactly once with the disable reason.
	disabled := fakeNotifier.disabledSnapshot()
	require.Len(t, disabled, 1)
	assert.Equal(t, "f1", disabled[0].AutomationID)
	assert.Equal(t, "deactivated-user", disabled[0].CreatedBy)
	assert.Contains(t, disabled[0].Reason, "deactivated")
	// A disable is not an execution failure, so no failure DM should fire.
	assert.Empty(t, fakeNotifier.snapshot())
}

func TestWorkerPool_CreatorPermissionDemoted(t *testing.T) {
	act := &testAction{}

	store, _ := setupStore(t)
	api := &plugintest.API{}
	api.On("LogInfo", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	api.On("LogWarn", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	api.On("LogError", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	api.On("GetUser", "demoted-user").Return(&mmmodel.User{DeleteAt: 0}, nil)
	// User is no longer a system admin.
	api.On("HasPermissionTo", "demoted-user", mock.Anything).Return(false)
	api.On("GetChannel", "ch1").Return(&mmmodel.Channel{Id: "ch1", Type: mmmodel.ChannelTypeOpen}, nil)
	api.On("GetChannelMember", "ch1", "demoted-user").Return(&mmmodel.ChannelMember{}, nil)
	api.On("HasPermissionToChannel", "demoted-user", "ch1", mmmodel.PermissionManageChannelRoles).Return(false)

	registry := automation.NewRegistry()
	registry.RegisterAction(act)
	executor := automation.NewExecutor(registry)
	automationStore := newTestAutomationStore()

	wp := NewWorkerPool(store, executor, automationStore, nil, nil, api, 4)
	wp.pollInterval = 50 * time.Millisecond

	_ = automationStore.Save(&model.Automation{
		ID: "f1", Name: "Automation 1", Enabled: true, CreatedBy: "demoted-user",
		Trigger: model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch1"}},
		Actions: []model.Action{{ID: "a1", SendMessage: &model.SendMessageActionConfig{ChannelID: "ch1", Body: "hi"}}},
	})

	item := &model.WorkItem{ID: "w1", AutomationID: "f1", AutomationName: "Automation 1"}
	require.NoError(t, store.Enqueue(item))

	wp.Start()
	wp.Notify()

	require.Eventually(t, func() bool {
		got, _ := store.Get("w1")
		return got == nil
	}, 5*time.Second, 10*time.Millisecond)

	wp.Stop()

	// Action should never have been called.
	assert.Equal(t, 0, act.getExecCount())

	// Automation should be disabled — creator lost permissions.
	f, _ := automationStore.Get("f1")
	require.NotNil(t, f)
	assert.False(t, f.Enabled)
}

func TestWorkerPool_CreatorPermissionCheckTransientError(t *testing.T) {
	act := &testAction{}

	store, _ := setupStore(t)
	api := &plugintest.API{}
	api.On("LogInfo", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	api.On("LogWarn", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	api.On("LogError", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	api.On("GetUser", "some-user").Return(&mmmodel.User{DeleteAt: 0}, nil)
	// User is not a system admin.
	api.On("HasPermissionTo", "some-user", mock.Anything).Return(false)
	// GetChannel returns a 500 — transient infrastructure error.
	api.On("GetChannel", "ch1").Return(nil, mmmodel.NewAppError("GetChannel", "app.channel.get.app_error", nil, "", 500))

	registry := automation.NewRegistry()
	registry.RegisterAction(act)
	executor := automation.NewExecutor(registry)
	automationStore := newTestAutomationStore()

	wp := NewWorkerPool(store, executor, automationStore, nil, nil, api, 4)
	wp.pollInterval = 50 * time.Millisecond

	_ = automationStore.Save(&model.Automation{
		ID: "f1", Name: "Automation 1", Enabled: true, CreatedBy: "some-user",
		Trigger: model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch1"}},
		Actions: []model.Action{{ID: "a1", SendMessage: &model.SendMessageActionConfig{ChannelID: "ch1", Body: "hi"}}},
	})

	item := &model.WorkItem{ID: "w1", AutomationID: "f1", AutomationName: "Automation 1"}
	require.NoError(t, store.Enqueue(item))

	wp.Start()
	wp.Notify()

	require.Eventually(t, func() bool {
		got, _ := store.Get("w1")
		return got == nil
	}, 5*time.Second, 10*time.Millisecond)

	wp.Stop()

	// Action should never have been called.
	assert.Equal(t, 0, act.getExecCount())

	// Automation should remain enabled — this is a transient error.
	f, _ := automationStore.Get("f1")
	require.NotNil(t, f)
	assert.True(t, f.Enabled)
}

func TestWorkerPool_GuardrailsBecameRequired_Disables(t *testing.T) {
	act := &testAction{}

	store, _ := setupStore(t)
	api := &plugintest.API{}
	api.On("LogInfo", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	api.On("LogWarn", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	api.On("LogError", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	api.On("GetUser", "creator1").Return(&mmmodel.User{DeleteAt: 0}, nil)
	api.On("HasPermissionTo", "creator1", mock.Anything).Return(true)
	// A private channel that has since gained a second member.
	api.On("GetChannel", "ch1").Return(&mmmodel.Channel{Id: "ch1", Type: mmmodel.ChannelTypePrivate}, nil)
	api.On("GetChannelMember", "ch1", "creator1").Return(&mmmodel.ChannelMember{}, nil)
	api.On("GetChannelStats", "ch1").Return(&mmmodel.ChannelStats{MemberCount: 2}, nil)

	registry := automation.NewRegistry()
	registry.RegisterAction(act)
	executor := automation.NewExecutor(registry)
	automationStore := newTestAutomationStore()

	wp := NewWorkerPool(store, executor, automationStore, nil, nil, api, 4)
	wp.pollInterval = 50 * time.Millisecond

	_ = automationStore.Save(&model.Automation{
		ID: "f1", Name: "Automation 1", Enabled: true, CreatedBy: "creator1",
		Trigger: model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch1"}},
		Actions: []model.Action{{ID: "a1", AIPrompt: &model.AIPromptActionConfig{
			Prompt: "summarize", ProviderType: "agent", ProviderID: "bot1",
			AllowedTools: []string{"search_posts"},
		}}},
	})

	item := &model.WorkItem{ID: "w1", AutomationID: "f1", AutomationName: "Automation 1"}
	require.NoError(t, store.Enqueue(item))

	wp.Start()
	wp.Notify()

	require.Eventually(t, func() bool {
		got, _ := store.Get("w1")
		return got == nil
	}, 5*time.Second, 10*time.Millisecond)

	wp.Stop()

	// Action must never run when the context became sensitive without guardrails.
	assert.Equal(t, 0, act.getExecCount())

	// Automation should be disabled — it now requires guardrails but has none.
	f, _ := automationStore.Get("f1")
	require.NotNil(t, f)
	assert.False(t, f.Enabled)
}

func TestWorkerPool_UnguardedExemptForeignActor_Disables(t *testing.T) {
	act := &testAction{}

	store, _ := setupStore(t)
	api := &plugintest.API{}
	api.On("LogInfo", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	api.On("LogWarn", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	api.On("LogError", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	api.On("GetUser", "creator1").Return(&mmmodel.User{DeleteAt: 0}, nil)
	api.On("HasPermissionTo", "creator1", mock.Anything).Return(true)
	// Single-member private channel — exempt by context, but the queued event
	// carries a foreign triggerer, so CheckGuardrailsRequired still rejects it.
	api.On("GetChannel", "ch1").Return(&mmmodel.Channel{Id: "ch1", Type: mmmodel.ChannelTypePrivate}, nil)
	api.On("GetChannelMember", "ch1", "creator1").Return(&mmmodel.ChannelMember{}, nil)
	api.On("GetChannelStats", "ch1").Return(&mmmodel.ChannelStats{MemberCount: 1}, nil)

	registry := automation.NewRegistry()
	registry.RegisterAction(act)
	executor := automation.NewExecutor(registry)
	automationStore := newTestAutomationStore()

	wp := NewWorkerPool(store, executor, automationStore, nil, nil, api, 4)
	wp.pollInterval = 50 * time.Millisecond

	_ = automationStore.Save(&model.Automation{
		ID: "f1", Name: "Automation 1", Enabled: true, CreatedBy: "creator1",
		Trigger: model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch1"}},
		Actions: []model.Action{{ID: "a1", AIPrompt: &model.AIPromptActionConfig{
			Prompt: "summarize", ProviderType: "agent", ProviderID: "bot1",
			RequestAs:    model.AIPromptRequestAsTriggerer,
			AllowedTools: []string{"search_posts"},
		}}},
	})

	// The queued event captured a foreign triggerer before membership fell back
	// to a single member.
	item := &model.WorkItem{
		ID: "w1", AutomationID: "f1", AutomationName: "Automation 1",
		TriggerData: model.TriggerData{User: &model.SafeUser{Id: "intruder"}},
	}
	require.NoError(t, store.Enqueue(item))

	wp.Start()
	wp.Notify()

	require.Eventually(t, func() bool {
		got, _ := store.Get("w1")
		return got == nil
	}, 5*time.Second, 10*time.Millisecond)

	wp.Stop()

	// The action must not run under the foreign triggerer's identity.
	assert.Equal(t, 0, act.getExecCount())

	// The automation is disabled — a foreign triggerer is treated the same as a
	// context that became sensitive: fail closed until the creator re-saves.
	f, _ := automationStore.Get("f1")
	require.NotNil(t, f)
	assert.False(t, f.Enabled)
}

func TestWorkerPool_GuardrailsCheckTransientError(t *testing.T) {
	act := &testAction{}

	store, _ := setupStore(t)
	api := &plugintest.API{}
	api.On("LogInfo", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	api.On("LogWarn", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	api.On("LogError", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	api.On("GetUser", "creator1").Return(&mmmodel.User{DeleteAt: 0}, nil)
	api.On("HasPermissionTo", "creator1", mock.Anything).Return(true)
	api.On("GetChannel", "ch1").Return(&mmmodel.Channel{Id: "ch1", Type: mmmodel.ChannelTypePrivate}, nil)
	api.On("GetChannelMember", "ch1", "creator1").Return(&mmmodel.ChannelMember{}, nil)
	// GetChannelStats returns a 500 — transient infrastructure error.
	api.On("GetChannelStats", "ch1").Return(nil, mmmodel.NewAppError("GetChannelStats", "app.channel.get_stats.app_error", nil, "", 500))

	registry := automation.NewRegistry()
	registry.RegisterAction(act)
	executor := automation.NewExecutor(registry)
	automationStore := newTestAutomationStore()

	wp := NewWorkerPool(store, executor, automationStore, nil, nil, api, 4)
	wp.pollInterval = 50 * time.Millisecond

	_ = automationStore.Save(&model.Automation{
		ID: "f1", Name: "Automation 1", Enabled: true, CreatedBy: "creator1",
		Trigger: model.Trigger{MessagePosted: &model.MessagePostedConfig{ChannelID: "ch1"}},
		Actions: []model.Action{{ID: "a1", AIPrompt: &model.AIPromptActionConfig{
			Prompt: "summarize", ProviderType: "agent", ProviderID: "bot1",
			AllowedTools: []string{"search_posts"},
		}}},
	})

	item := &model.WorkItem{ID: "w1", AutomationID: "f1", AutomationName: "Automation 1"}
	require.NoError(t, store.Enqueue(item))

	wp.Start()
	wp.Notify()

	require.Eventually(t, func() bool {
		got, _ := store.Get("w1")
		return got == nil
	}, 5*time.Second, 10*time.Millisecond)

	wp.Stop()

	// Action must not run while the sensitivity of the context is unverifiable.
	assert.Equal(t, 0, act.getExecCount())

	// Automation should remain enabled — this is a transient error.
	f, _ := automationStore.Get("f1")
	require.NotNil(t, f)
	assert.True(t, f.Enabled)
}

// fakeFailureNotifier records all NotifyFailure and NotifyDisabled calls for assertions.
type fakeFailureNotifier struct {
	mu            sync.Mutex
	calls         []notifier.FailureDetails
	disabledCalls []notifier.DisabledDetails
}

func (f *fakeFailureNotifier) NotifyFailure(d notifier.FailureDetails) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, d)
}

func (f *fakeFailureNotifier) NotifyDisabled(d notifier.DisabledDetails) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disabledCalls = append(f.disabledCalls, d)
}

func (f *fakeFailureNotifier) snapshot() []notifier.FailureDetails {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]notifier.FailureDetails, len(f.calls))
	copy(out, f.calls)
	return out
}

func (f *fakeFailureNotifier) disabledSnapshot() []notifier.DisabledDetails {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]notifier.DisabledDetails, len(f.disabledCalls))
	copy(out, f.disabledCalls)
	return out
}

func TestWorkerPool_NotifierInvokedOnActionFailure(t *testing.T) {
	failingAct := &testAction{execFn: func() error { return fmt.Errorf("simulated failure") }}

	store, _ := setupStore(t)
	api := &plugintest.API{}
	api.On("LogInfo", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	api.On("LogWarn", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	api.On("LogError", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	api.On("GetUser", mock.Anything).Return(&mmmodel.User{DeleteAt: 0}, nil)
	api.On("HasPermissionTo", mock.Anything, mock.Anything).Return(true)

	registry := automation.NewRegistry()
	registry.RegisterAction(failingAct)
	executor := automation.NewExecutor(registry)
	automationStore := newTestAutomationStore()
	notifier := &fakeFailureNotifier{}

	wp := NewWorkerPool(store, executor, automationStore, nil, notifier, api, 4)
	wp.pollInterval = 50 * time.Millisecond

	_ = automationStore.Save(&model.Automation{
		ID: "f1", Name: "My Automation", Enabled: true, CreatedBy: "creator1",
		Actions: []model.Action{{ID: "a1", SendMessage: &model.SendMessageActionConfig{}}},
	})

	item := &model.WorkItem{ID: "w1", AutomationID: "f1", AutomationName: "My Automation"}
	require.NoError(t, store.Enqueue(item))

	wp.Start()
	wp.Notify()

	require.Eventually(t, func() bool {
		got, _ := store.Get("w1")
		return got == nil
	}, 5*time.Second, 10*time.Millisecond)

	wp.Stop()

	calls := notifier.snapshot()
	require.Len(t, calls, 1, "expected one failure notification")
	d := calls[0]
	assert.Equal(t, "f1", d.AutomationID)
	assert.Equal(t, "My Automation", d.AutomationName)
	assert.Equal(t, "creator1", d.CreatedBy)
	assert.Equal(t, "a1", d.ActionID)
	assert.Equal(t, "send_message", d.ActionType)
	assert.Equal(t, "w1", d.ExecutionID)
	assert.Equal(t, "simulated failure", d.ErrorMsg)
}

func TestWorkerPool_NotifierNotInvokedOnSuccess(t *testing.T) {
	act := &testAction{}
	notifier := &fakeFailureNotifier{}

	store, _ := setupStore(t)
	api := &plugintest.API{}
	api.On("LogInfo", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	api.On("LogWarn", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	api.On("LogError", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
	api.On("GetUser", mock.Anything).Return(&mmmodel.User{DeleteAt: 0}, nil)
	api.On("HasPermissionTo", mock.Anything, mock.Anything).Return(true)

	registry := automation.NewRegistry()
	registry.RegisterAction(act)
	executor := automation.NewExecutor(registry)
	automationStore := newTestAutomationStore()

	wp := NewWorkerPool(store, executor, automationStore, nil, notifier, api, 4)
	wp.pollInterval = 50 * time.Millisecond

	_ = automationStore.Save(&model.Automation{ID: "f1", Name: "Automation 1", Enabled: true, Actions: []model.Action{{ID: "a1", SendMessage: &model.SendMessageActionConfig{}}}})

	item := &model.WorkItem{ID: "w1", AutomationID: "f1", AutomationName: "Automation 1"}
	require.NoError(t, store.Enqueue(item))

	wp.Start()
	wp.Notify()

	require.Eventually(t, func() bool {
		got, _ := store.Get("w1")
		return got == nil
	}, 5*time.Second, 10*time.Millisecond)

	wp.Stop()

	assert.Empty(t, notifier.snapshot(), "expected no failure notifications on success")
}

func TestWorkerPool_DisabledAutomation(t *testing.T) {
	act := &testAction{}
	wp, store, automationStore := setupWorkerPool(t, 4, act)

	_ = automationStore.Save(&model.Automation{ID: "f1", Name: "Automation 1", Enabled: false, Actions: []model.Action{{ID: "a1", SendMessage: &model.SendMessageActionConfig{}}}})

	item := &model.WorkItem{ID: "w1", AutomationID: "f1", AutomationName: "Automation 1"}
	require.NoError(t, store.Enqueue(item))

	wp.Start()
	wp.Notify()

	// Wait for processing.
	require.Eventually(t, func() bool {
		got, _ := store.Get("w1")
		return got == nil
	}, 5*time.Second, 10*time.Millisecond)

	wp.Stop()

	// Action should never have been called.
	assert.Equal(t, 0, act.getExecCount())
}
