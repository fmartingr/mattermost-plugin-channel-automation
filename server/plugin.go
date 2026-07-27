package main

import (
	"fmt"
	"net/http"
	"sync"

	"github.com/gorilla/mux"
	"github.com/mattermost/mattermost-plugin-agents/public/bridgeclient"
	mmmodel "github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"
	"github.com/mattermost/mattermost/server/public/pluginapi"
	"github.com/mattermost/mattermost/server/public/pluginapi/cluster"

	"github.com/mattermost/mattermost-plugin-channel-automation/server/automation"
	"github.com/mattermost/mattermost-plugin-channel-automation/server/automation/action"
	"github.com/mattermost/mattermost-plugin-channel-automation/server/automation/notifier"
	"github.com/mattermost/mattermost-plugin-channel-automation/server/automation/trigger"
	"github.com/mattermost/mattermost-plugin-channel-automation/server/command"
	"github.com/mattermost/mattermost-plugin-channel-automation/server/execution"
	"github.com/mattermost/mattermost-plugin-channel-automation/server/model"
	"github.com/mattermost/mattermost-plugin-channel-automation/server/workqueue"
)

// Plugin implements the interface expected by the Mattermost server to communicate between the server and plugin processes.
type Plugin struct {
	plugin.MattermostPlugin

	// client is the Mattermost server API client.
	client *pluginapi.Client

	// commandClient is the client used to register and execute slash commands.
	commandClient command.Command

	// router is the HTTP router for handling API requests.
	router *mux.Router

	workQueueStore *workqueue.Store
	workerPool     *workqueue.WorkerPool

	bridgeClient *bridgeclient.Client

	botUserID          string
	registry           *automation.Registry
	automationStore    model.Store
	historyStore       model.ExecutionStore
	triggerService     *automation.TriggerService
	automationExecutor *automation.Executor
	scheduleManager    *automation.ScheduleManager
	dispatcher         *automation.Dispatcher

	// configurationLock synchronizes access to the configuration.
	configurationLock sync.RWMutex

	// configuration is the active plugin configuration. Consult getConfiguration and
	// setConfiguration for usage.
	configuration *configuration
}

// OnActivate is invoked when the plugin is activated. If an error is returned, the plugin will be deactivated.
func (p *Plugin) OnActivate() error {
	if !pluginapi.IsEnterpriseLicensedOrDevelopment(p.API.GetConfig(), p.API.GetLicense()) {
		return fmt.Errorf("this plugin requires an Enterprise license")
	}

	p.client = pluginapi.NewClient(p.API, p.Driver)

	botUserID, err := p.client.Bot.EnsureBot(&mmmodel.Bot{
		Username:    "channel-automation-bot",
		DisplayName: "Channel Automation",
		Description: "I can help you automate things in channels.",
	})
	if err != nil {
		return err
	}
	p.botUserID = botUserID

	bc := bridgeclient.NewClient(p.API)
	p.bridgeClient = bc

	// TODO: Register tools in the bridge client

	p.registry = automation.NewRegistry()
	p.registry.RegisterTrigger(&trigger.MessagePostedTrigger{})
	p.registry.RegisterTrigger(&trigger.ScheduleTrigger{})
	p.registry.RegisterTrigger(&trigger.MembershipChangedTrigger{})
	p.registry.RegisterTrigger(&trigger.ChannelCreatedTrigger{})
	p.registry.RegisterTrigger(&trigger.UserJoinedTeamTrigger{})
	p.registry.RegisterAction(action.NewSendMessageAction(p.API, p.botUserID))
	p.registry.RegisterAction(action.NewAIPromptAction(p.API, bc, p.botUserID))

	automationIndexMu, err := cluster.NewMutex(p.API, "automation_index_mutex")
	if err != nil {
		return fmt.Errorf("failed to create automation index mutex: %w", err)
	}
	p.automationStore = automation.NewStore(p.API, automationIndexMu)
	p.triggerService = automation.NewTriggerService(p.automationStore, p.registry)
	p.automationExecutor = automation.NewExecutor(p.registry)

	// Set up persistent work queue.
	indexMu, err := cluster.NewMutex(p.API, "wq_index_mutex")
	if err != nil {
		return fmt.Errorf("failed to create work queue mutex: %w", err)
	}
	p.workQueueStore = workqueue.NewStore(p.API, indexMu)

	xhIndexMu, err := cluster.NewMutex(p.API, "xh_index_mutex")
	if err != nil {
		return fmt.Errorf("failed to create execution history mutex: %w", err)
	}
	p.historyStore = execution.NewStore(p.API, xhIndexMu)

	resetCount, err := p.workQueueStore.ResetRunningToPending()
	if err != nil {
		p.API.LogError("Failed to reset running work items", "err", err.Error())
	} else if resetCount > 0 {
		p.API.LogInfo("Reset orphaned work items to pending", "count", resetCount)
	}

	maxWorkers := p.getConfiguration().MaxConcurrentAutomationsLimit
	if maxWorkers <= 0 {
		maxWorkers = 4
	}

	cooldownStore := notifier.NewCooldownStore(p.API, notifier.NotificationCooldown)
	failureNotifier := notifier.NewCreatorNotifier(p.API, cooldownStore, p.botUserID)
	p.workerPool = workqueue.NewWorkerPool(p.workQueueStore, p.automationExecutor, p.automationStore, p.historyStore, failureNotifier, p.API, maxWorkers)
	p.workerPool.Start()

	p.dispatcher = automation.NewDispatcher(p.API, p.triggerService, p.workQueueStore, p.workerPool)

	// Start schedule manager after worker pool so scheduled items can be processed.
	p.scheduleManager = automation.NewScheduleManager(p.API, p.automationStore, p.workQueueStore, p.workerPool)
	if err := p.scheduleManager.Start(); err != nil {
		p.API.LogError("Failed to start schedule manager", "err", err.Error())
	}

	// Initialize router last — it depends on scheduleManager.
	p.router = p.initRouter()

	return nil
}

// OnDeactivate is invoked when the plugin is deactivated.
func (p *Plugin) OnDeactivate() error {
	// Stop schedule manager first — it may be enqueuing items.
	if p.scheduleManager != nil {
		p.scheduleManager.Stop()
	}
	if p.workerPool != nil {
		p.workerPool.Stop()
	}
	return nil
}

// This will execute the commands that were registered in the NewCommandHandler function.
func (p *Plugin) ExecuteCommand(c *plugin.Context, args *mmmodel.CommandArgs) (*mmmodel.CommandResponse, *mmmodel.AppError) {
	response, err := p.commandClient.Handle(args)
	if err != nil {
		return nil, mmmodel.NewAppError("ExecuteCommand", "plugin.command.execute_command.app_error", nil, err.Error(), http.StatusInternalServerError)
	}
	return response, nil
}

// MessageHasBeenPosted is invoked after a message is posted.
func (p *Plugin) MessageHasBeenPosted(_ *plugin.Context, post *mmmodel.Post) {
	// Filter bot/system/webhook/AI posts to prevent loops and unintended triggers.
	if post.UserId == p.botUserID {
		return
	}
	if post.IsSystemMessage() {
		return
	}
	if post.GetProp("from_webhook") == "true" {
		return
	}
	if post.GetProp("from_bot") == "true" {
		return
	}
	if post.GetProp("ai_generated_by") != nil {
		return
	}

	// Defense in depth: from_bot is a convention, not a guarantee. Resolve the
	// author and drop bot accounts the same way membership_changed does, so a
	// bot post that somehow lacks the prop cannot become an ai_prompt triggerer.
	user, appErr := p.API.GetUser(post.UserId)
	if appErr != nil {
		p.API.LogError("Failed to get user for message_posted trigger", "user_id", post.UserId, "err", appErr.Error())
		return
	}
	if user.IsBot {
		return
	}

	p.dispatcher.Dispatch(&model.Event{
		Type: model.TriggerTypeMessagePosted,
		Post: post,
	})
}

// UserHasJoinedChannel is invoked after a user joins a channel.
func (p *Plugin) UserHasJoinedChannel(_ *plugin.Context, member *mmmodel.ChannelMember, _ *mmmodel.User) {
	p.handleMembershipChange(member, "joined")
}

// UserHasLeftChannel is invoked after a user leaves a channel.
func (p *Plugin) UserHasLeftChannel(_ *plugin.Context, member *mmmodel.ChannelMember, _ *mmmodel.User) {
	p.handleMembershipChange(member, "left")
}

// handleMembershipChange is the shared logic for join/leave hooks. It
// resolves the user and channel up front because the membership filter
// needs to inspect the user (bot check) before dispatching.
func (p *Plugin) handleMembershipChange(member *mmmodel.ChannelMember, action string) {
	if member.UserId == p.botUserID {
		return
	}

	user, appErr := p.API.GetUser(member.UserId)
	if appErr != nil {
		p.API.LogError("Failed to get user for membership trigger", "user_id", member.UserId, "err", appErr.Error())
		return
	}
	if user.IsBot {
		return
	}

	channel, appErr := p.API.GetChannel(member.ChannelId)
	if appErr != nil {
		p.API.LogError("Failed to get channel for membership trigger", "channel_id", member.ChannelId, "err", appErr.Error())
		return
	}

	p.dispatcher.Dispatch(&model.Event{
		Type:             model.TriggerTypeMembershipChanged,
		Channel:          channel,
		User:             user,
		MembershipAction: action,
	})
}

// ChannelHasBeenCreated is invoked after a new channel is created.
// Only public channels (type "O") trigger automations.
func (p *Plugin) ChannelHasBeenCreated(_ *plugin.Context, channel *mmmodel.Channel) {
	if channel.Type != mmmodel.ChannelTypeOpen {
		return
	}

	p.dispatcher.Dispatch(&model.Event{
		Type:    model.TriggerTypeChannelCreated,
		Channel: channel,
	})
}

// UserHasJoinedTeam is invoked after a user joins a team.
// The actor parameter (who performed the action) is ignored — we always
// resolve the joining user from teamMember.UserId, matching the pattern
// used by UserHasJoinedChannel/UserHasLeftChannel.
func (p *Plugin) UserHasJoinedTeam(_ *plugin.Context, teamMember *mmmodel.TeamMember, _ *mmmodel.User) {
	if teamMember.UserId == p.botUserID {
		return
	}

	user, appErr := p.API.GetUser(teamMember.UserId)
	if appErr != nil {
		p.API.LogError("Failed to get user for team join trigger", "user_id", teamMember.UserId, "err", appErr.Error())
		return
	}
	if user.IsBot {
		return
	}

	p.dispatcher.Dispatch(&model.Event{
		Type: model.TriggerTypeUserJoinedTeam,
		Team: &mmmodel.Team{Id: teamMember.TeamId},
		User: user,
	})
}

// See https://developers.mattermost.com/extend/plugins/server/reference/
