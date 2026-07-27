package main

import (
	"encoding/json"
	"net/http"
	"strings"
)

// channelAutomationInstructionsBase documents trigger types, actions, templates, and the
// confirmation protocol for create_automation and update_automation (returned by GET /automation-instructions).
const channelAutomationInstructionsBase = `Channel automations are trigger-action workflows that fire when events occur.

PERMISSION PREFLIGHT — VERIFY BEFORE DOING ANYTHING ELSE:
The user must have permission to create an automation for the trigger they are asking about, otherwise create_automation will fail. Verify up-front so you don't waste the user's time gathering details for an automation they cannot create. The user is permitted when ANY of the following is true:
- They are a system administrator. The agent context surfaces this as "System role: System Administrator" — if you see that line, permission is granted; skip the rest of this preflight.
- The trigger fires in a direct message (DM) or group message (GM) channel and the user is a participant. DMs/GMs have no channel-admin role, so any participant may create an automation for that channel.
- They can manage channel roles on the trigger channel (channel admin or team admin via scheme resolution) for message_posted, schedule, and membership_changed triggers in regular public/private channels.
- They are a team admin of the trigger team (for the channel_created and user_joined_team triggers, which are scoped by team_id rather than channel_id).

How to check the channel role: call get_channel_info with the trigger channel_id. The response includes the requesting user's role in that channel as one of "admin", "member", "guest", or "not_member". "admin" satisfies the manage-channel-roles requirement. Channel type "D" (DM) or "G" (GM) on the returned channel also satisfies the requirement for any participant. Team admins of the channel's team also satisfy this requirement in Mattermost even when their channel role is "member". For team-scoped triggers (channel_created, user_joined_team) you cannot verify team admin from get_channel_info; tell the user that team admin permission on the target team is required and proceed only if they confirm they have it (or are a system admin).

If the user does NOT have permission, stop and explain which role they need (system admin, channel admin of <channel>, team admin of <team>, or that the trigger must be in a DM/GM).

AGENT DISCOVERY: For an ai_prompt action with provider_type "agent", use the list_agents tool to discover bots.
Each agent's ID is a 26-character Mattermost user ID — use that value as provider_id in the ai_prompt config.

IMPORTANT WORKFLOW — ALWAYS CONFIRM BEFORE CREATING:
Before calling create_automation (or update_automation), you MUST present a plain-language summary to the user and get their
explicit confirmation. Even if the user provided all details, always present the full summary.

Keep all setup conversation in the current thread. Do not call dm, group_message, create_post, send a direct
message, or send any other out-of-band message while gathering details, verifying usernames, or confirming
recipients. If you need to resolve usernames, use read-only lookup tools such as search_users and present the
resolved users in the current thread as part of a clarifying question or the required summary.

The summary MUST use exactly four numbered items in this order (do not merge or skip any;
users rely on this structure to understand risk before they confirm):
1. TRIGGER: What event fires this automation and its scope.
2. AI TOOLS: Which tools the AI agent will have access to and what each one can do.
   - Without tools, the agent can only generate text from its built-in knowledge — it cannot
     read any Mattermost data or take any actions.
   - With tools, the agent inherits the permissions of the user the request is attributed to
     (controlled by request_as — see below). By default the request is attributed to the user
     who triggered the automation ("triggerer") and the agent acts with that user's permissions;
     when request_as is "creator", the agent acts with the automation creator's permissions
     regardless of who triggered it. State explicitly which identity the tools will run as so
     the user understands the access they are granting (especially when request_as is "creator"
     in a shared channel — every triggerer will exercise the creator's permissions).
3. GUARDRAILS: Dedicated step — do not fold this into item 2. Reference the "MATTERMOST MCP CHANNEL
   GUARDRAILS" list below. For each ai_prompt step that uses allowed_tools AND includes at least
   one guardrail-constrained Mattermost tool name (channel- or team-scoped), state the exact
   guardrails.channel_ids you will set (list each 26-char channel ID or say you will use the
   trigger channel / template such as {{.Trigger.Channel.Id}}). If no ai_prompt step uses
   allowed_tools, write "Not applicable — no MCP tools in this automation." If allowed_tools uses
   only external MCP tools and/or unconstrained Mattermost tools (none of the constrained names
   below), write "Not applicable — no guardrail-constrained Mattermost MCP tools in this
   automation." Otherwise explain why the chosen channel_ids limit Mattermost tool reads/writes
   to what the user should expect.
   GUARDRAILS ARE REQUIRED (server-enforced) for any ai_prompt step that includes a
   guardrail-constrained Mattermost tool (the constrained names listed below) when the trigger
   context is "sensitive": public channels, private channels with more than one member, group
   messages (GMs), DMs whose other participant is not a bot, or any team-scoped trigger
   (channel_created, user_joined_team). In these cases the save will be rejected with 403 unless
   guardrails.channel_ids is non-empty — collect the channel_ids from the user up front.
   Exempt contexts (guardrails optional): DM with the bot itself, self-DM, single-member private
   channel, or an ai_prompt whose allowed_tools has no guardrail-constrained Mattermost tool.
4. OUTPUT: Where the automation will post results — name the specific channel(s) and include the exact
   message body/template for every send_message action, preserving any template variables so the user can
   verify the content before it is saved.

Format as that four-part numbered list (1–4), then ask the user to confirm. Only call create_automation after
the user says yes.

If the user's request is missing details (trigger channel, output channel, message body/template, which tools,
or for schedule triggers the first run day/date, time, and timezone when the recurrence is vague),
ask clarifying questions BEFORE presenting the summary.

MATTERMOST MCP CHANNEL GUARDRAILS — WHICH TOOL NAMES ARE CONSTRAINED:
Channel guardrails (guardrails.channel_ids on an ai_prompt) constrain a fixed set of built-in
Mattermost MCP tools. The exact tool names below are matched against tool discovery output;
any other tool name (including external MCP tools) passes through unchanged when guardrails are set.

TOOL NAME FORMS: Tool discovery may return a Mattermost tool either by its bare name
(e.g. "search_posts") or namespaced with its MCP server slug (e.g. "mattermost__search_posts").
The names listed in this section are the bare names; a discovered tool is the same constrained
tool whether it appears bare or as "mattermost__<name>". Either form is accepted in allowed_tools
and guardrails apply to both, so when classifying a discovered tool, compare it against the bare
names below after stripping any "mattermost__" prefix.

Channel-scoped — restricted to guardrails.channel_ids. Tools differ in how they're
constrained:

Tools that REQUIRE a channel_id argument matching guardrails.channel_ids:
- search_posts
- read_channel
- get_channel_members
- add_user_to_channel
- get_channel_info

Tools that take a post_id; the post's resolved channel_id must be in guardrails.channel_ids:
- read_post

Tools that are NOT permitted when channel guardrails are configured (the
automation save/run will reject them):
- get_user_channels

Team-scoped — the tool's team_id argument must be the team that owns one of the allowed
channels (channel→team is resolved automatically from guardrails.channel_ids), or the
trigger team for channel_created/user_joined_team automations:
- get_team_info
- get_team_members
- create_channel (the new channel must be created in an allowed team)

Other built-in Mattermost MCP tools (search_users, list_agents) may be used in allowed_tools
but are NOT constrained by channel guardrails — they execute with the automation owner's full
permissions regardless of guardrails.channel_ids. Mention this explicitly in summary item 2
when granting them.

Mutating tools that act on the user's behalf (create_post, dm, group_message) and the
automation-management tools (list_automations, get_automation_instructions, create_automation,
update_automation, delete_automation) are rejected from allowed_tools entirely — guardrails
do not unlock them. Use a send_message action instead of granting create_post; there is no
built-in action for posting DMs or group messages, so configure the trigger to fire in the
target DM/GM channel and use send_message there. Never include the automation-management tools.

External MCP tools (anything not in the lists above) are unaffected by channel guardrails and
pass through unchanged. You may freely mix external tools with Mattermost tools in a single
ai_prompt step's allowed_tools — there is no need to split into multiple ai_prompt steps just
to combine them. Hook URLs are only registered for the constrained Mattermost tool names above,
so unknown names do not cause runtime failures.

If every tool in allowed_tools across all ai_prompt steps is external-only or in the
unconstrained Mattermost set (none of the constrained names above), omit guardrails — use
summary item 3 to say "Not applicable — no guardrail-constrained Mattermost MCP tools in
this automation."

ACTION SELECTION: For each step in the automation, choose the right action type:
- send_message: for posting text to channels.
- ai_prompt with allowed_tools: for anything else — any step that needs to read data, modify state, or interact with Mattermost beyond posting text. Discover tools via the AI bridge GET .../agents/{id}/tools (or list_tools); each allowed_tools entry is the tool name string from discovery exactly as returned (e.g. "search_posts", which discovery may also return namespaced as "mattermost__search_posts" — either form is accepted).
If a step cannot be accomplished with send_message, it MUST be an ai_prompt action with the appropriate tools.

TOOL SUFFICIENCY CHECK (THIS IS VERY IMPORTANT): Before presenting the summary, think through the automation's task
step-by-step and verify the granted tools cover every step the agent will need to perform.
Ask: what data does the agent need to discover, read, or act on — and can it actually do
each of those things with only the tools listed? If any step requires a tool that isn't
included, add it to your recommendation and explain why it's needed.

TRIGGERS: Set exactly one trigger type inside the "trigger" object.
- "message_posted": fires when a human user posts a message in the channel. Bot messages are automatically filtered out, so there is no risk of bot-triggered loops. High-traffic channels will trigger frequently.
  {"trigger": {"message_posted": {"channel_id": "<channel-id>"}}}
- "schedule": fires on a recurring schedule.
  - interval: Go duration string (minimum "1h"). Examples: "1h" (hourly), "24h" (daily), "168h" (weekly).
  - start_at (optional): unix timestamp in milliseconds (UTC) for the first run — must be in the future. The automation fires at this time, then repeats every interval. If omitted, the first run happens immediately; only omit start_at when the user explicitly wants the first run immediate. Vague recurrences such as "every day", "every week", or "every N days" are missing timing unless the user also specified when the first run should happen — ask for the exact day/date, time, and timezone, set start_at to that future timestamp, and in summary item 1 include the recurring interval and exact first run day/date, time, and timezone (or state that the first run is immediate).
  {"trigger": {"schedule": {"channel_id": "<channel-id>", "interval": "24h", "start_at": 1899936000000}}}
- "membership_changed": fires when a member joins or leaves the channel.
  {"trigger": {"membership_changed": {"channel_id": "<channel-id>"}}}
- "channel_created": fires when a new public channel is created on the specified team. Requires team_id; fires for every new public channel created on that team by any user.
  {"trigger": {"channel_created": {"team_id": "<team-id>"}}}
- "user_joined_team": fires when a non-bot user joins the specified team.
  {"trigger": {"user_joined_team": {"team_id": "<team-id>"}}}

ACTIONS: Ordered array executed sequentially. Each action has a unique "id" (lowercase alphanumeric and hyphens only, e.g. "generate-recap" not "generate_recap") and exactly one action config.
Action types:
1. "send_message": Posts a message as a bot.
   {"id": "post", "send_message": {"channel_id": "<ch>", "body": "Hello!", "reply_to_post_id": "<optional post id>", "as_bot_id": "<optional bot user id>"}}
   - as_bot_id (optional): the Mattermost user ID of the bot to post as. Must be a bot account. If omitted, the message is posted as the default automation bot. Use list_agents to find bot IDs. When chaining after an ai_prompt action, set this to the same agent's user ID so the message appears to come from that agent.
2. "ai_prompt": Runs an AI agent with a prompt and optional tools. With tools, the agent can perform actions (e.g. modify channels, manage members, search) — not just generate text. Does NOT post a message — chain a send_message action after to post the response.
   {"id": "ask", "ai_prompt": {"prompt": "...", "provider_type": "agent", "provider_id": "<agent-user-id>", "system_prompt": "...", "allowed_tools": ["<tool name from discovery>", "..."], "guardrails": {"channel_ids": ["<26-char channel id>", "..."]}, "request_as": "triggerer"}}
   - provider_type: "agent" (a bot) or "service" (a raw LLM service)
   - provider_id: the agent's Mattermost user ID (26-char ID). Call list_agents to discover available agents and their IDs.
   - system_prompt (optional): system instructions for the AI
   - allowed_tools: list of tool name strings the AI agent is allowed to call (names must match bridge/agent tools discovery exactly; discovery lists MCP and embedded tools only, not built-in Mattermost agent tools). WITHOUT this, the agent has NO tool access and can only generate text from its built-in knowledge — it cannot read any Mattermost data or take any actions. With tools, the agent inherits the permissions of the user the request is attributed to via request_as (default "triggerer" — the user who triggered the run; "creator" pins it to the automation creator). IMPORTANT: Only include tools the user has explicitly agreed to. Always explain what each tool does in your summary. Prefer the minimum set of tools needed.
   - guardrails (optional): {"channel_ids": ["<26-char channel id>", "..."]}. When channel_ids is
     non-empty AND allowed_tools is set, hooks constrain the built-in Mattermost MCP tools listed
     in the "MATTERMOST MCP CHANNEL GUARDRAILS" section below (channel-scoped tools like
     search_posts/read_channel/get_channel_info/etc., plus the team-scoped get_team_info and
     get_team_members which derive their allowed teams from the channels' teams). Omit guardrails
     when allowed_tools for that step contains no constrained Mattermost names. External tools and
     unconstrained Mattermost tools (search_users, list_agents) may be mixed in
     the same step alongside constrained tools — they pass through unchanged. Prefer the trigger
     channel ID or a small explicit channel_ids set the user agrees to when guardrails apply.
     REQUIRED (server-enforced 403 on save) whenever allowed_tools is non-empty AND the trigger
     context is sensitive: public channels, private channels with >1 member, GMs, DMs with a
     non-bot user, or team-scoped triggers (channel_created, user_joined_team).
   - request_as (optional): selects which user the AI completion request — and therefore any
     tool calls made by the agent — is attributed to. Bounded enum, no arbitrary user IDs:
       - "triggerer" (default, also used when omitted): the user who triggered the automation
         (e.g. the post author for message_posted); falls back to the automation creator when
         the trigger has no associated user.
       - "creator": always the automation creator, regardless of who triggered the run.
     The agent runs with the resolved user's permissions, so picking "creator" in a shared
     channel lets every triggerer exercise the creator's access — only choose "creator" when
     the automation needs to act with the creator's elevated permissions and the user
     understands that. Any other value is rejected at create/update time.
     REQUEST_AS BY TRIGGER:
       - message_posted: "triggerer" (the post author) or "creator" — both allowed.
       - schedule: no triggering user, so the run always attributes to the creator regardless
         of request_as.
       - membership_changed, user_joined_team, channel_created: request_as MUST be "creator";
         "triggerer" or an unset value is rejected at create/update (and legacy automations
         still set to "triggerer" fail at execution).
     CREATOR-ONLY TOOLS: some built-in Mattermost tools may run ONLY as the creator and are
     rejected (at create/update and execution) when request_as is "triggerer" or unset —
     add_user_to_channel and create_channel (they write on the acting user's behalf), and
     search_users, list_agents, and get_user_channels (they cannot be constrained by channel
     guardrails). If an automation needs any of these, set request_as to "creator".
   TOOL SELECTION: Use bridge agent tools discovery or list_tools; copy each tool's name from the response.
   DYNAMIC DISCOVERY: The AI agent can use its tools at runtime to discover resources (e.g., find channels, look up users) — don't hardcode IDs into the prompt when the agent can discover them dynamically each run. This keeps automations resilient to changes like new channels being added.
   NOTE: "web_search" is NOT a valid tool name in allowed_tools. Web search is a native provider feature that works automatically if the agent has it enabled — do not include it in allowed_tools.
TEMPLATE SYNTAX: body, channel_id, reply_to_post_id, prompt, and system_prompt support Go text/template with this context.
For send_message channel_id, use one of: {{.Trigger.Channel.Id}}, {{.Trigger.Team.DefaultChannelId}}, or {{.Trigger.Post.ChannelId}}. Channel-bound triggers (message_posted, schedule, membership_changed) should use {{.Trigger.Channel.Id}}. Team-scoped triggers (channel_created, user_joined_team) have no bound channel — use {{.Trigger.Team.DefaultChannelId}} or an explicit channel ID literal instead.
- {{.Trigger.Post.Message}}, {{.Trigger.Post.Id}}, {{.Trigger.Post.ChannelId}}
- {{.Trigger.Channel.Id}}, {{.Trigger.Channel.Name}}, {{.Trigger.Channel.DisplayName}}
- {{.Trigger.User.Id}}, {{.Trigger.User.Username}}, {{.Trigger.User.FirstName}}, {{.Trigger.User.LastName}}
- {{.Trigger.Team.Id}}, {{.Trigger.Team.Name}}, {{.Trigger.Team.DisplayName}}, {{.Trigger.Team.DefaultChannelId}}
- {{(index .Steps "prev-action-id").Message}}, {{(index .Steps "prev-action-id").PostID}} — output from a previous action

CHAINING ACTIONS: A single ai_prompt action can call tools multiple times AND generate a text response in one step — prefer consolidating related work into one ai_prompt rather than splitting into many actions. Use {{(index .Steps "prev-action-id").Message}} in later actions to reference the text output of a previous ai_prompt.`

// automationInstructionsResponse is the JSON body for GET /automation-instructions.
type automationInstructionsResponse struct {
	Instructions string `json:"instructions"`
}

func buildAutomationInstructionsResponse(cfg *configuration) automationInstructionsResponse {
	instructions := channelAutomationInstructionsBase
	if cfg != nil {
		if u := strings.TrimSpace(cfg.AutomationInstructionsURL); u != "" {
			instructions += "\n\nFor more detailed documentation on creating automations, refer the user to: " + u
		}
	}
	return automationInstructionsResponse{Instructions: instructions}
}

// handleGetAutomationInstructions returns automation documentation for agents/MCP clients.
func (p *Plugin) handleGetAutomationInstructions(w http.ResponseWriter, r *http.Request) {
	cfg := p.getConfiguration()
	payload := buildAutomationInstructionsResponse(cfg)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		p.API.LogError("Failed to encode automation instructions", "error", err.Error())
	}
}
