package hooks

import (
	"maps"
	"slices"
	"strings"
)

// EmbeddedMattermostMCPOrigin identifies tools served by the Mattermost
// agents plugin's embedded MCP server. Mirrors mcp.EmbeddedClientKey from
// github.com/mattermost/mattermost-plugin-agents.
const EmbeddedMattermostMCPOrigin = "embedded://mattermost"

// MattermostMCPToolPrefix is the namespace prefix the agents bridge applies to
// embedded Mattermost MCP tools (e.g. "mattermost__read_channel").
const MattermostMCPToolPrefix = "mattermost__"

// BeforeFunc validates tool arguments before the resolver runs.
type BeforeFunc func(ctx HookCtx, args map[string]any) error

// MattermostMCPTool describes a known tool exposed by the Mattermost embedded
// MCP server.
//
// Allowed indicates whether the tool may appear in an automation's
// allowed_tools. Tools that bypass the controlled output path
// (create_post, dm, group_message) are present here with Allowed=false so
// this catalog stays the single source of truth for what's known and what's
// permitted.
//
// TriggererSafe marks tools that may run under request_as="triggerer". It is
// opt-in and fails closed: any tool left at the zero value is creator-only, so
// a newly added tool is never triggerer-usable by accident. Only channel-scoped
// read tools that channel guardrails can constrain are marked safe. Tools that
// borrow write authority from the acting user (add_user_to_channel,
// create_channel) or that guardrails cannot constrain (search_users,
// list_agents, get_user_channels) are left unsafe: under request_as=triggerer
// they would let a lower-privileged creator act with a higher-privileged
// triggerer's permissions.
//
// Before is an optional pre-call guardrail hook. The ai_prompt action only
// registers a callback URL with the bridge for tools that have a Before
// implementation. The HTTP handler still fails closed if it's called for a
// tool without a Before implementation, as defense in depth.
type MattermostMCPTool struct {
	Allowed       bool
	TriggererSafe bool
	Before        BeforeFunc
}

// mattermostMCPServerTools is the exhaustive set of tools registered by the
// Mattermost agents plugin built-in MCP server when dev mode is off
// (github.com/mattermost/mattermost-plugin-agents/mcpserver/tools — ProvideTools
// in provider.go).
//
// Dev-only tools (gated by devMode in the agents plugin) are intentionally
// omitted:
//   - create_user
//   - create_post_as_user
//   - create_team
//   - add_user_to_team
//
// Production Mattermost MCP tools (20), by area:
//
// Posts:       read_post, create_post, dm, group_message
// Channels:    read_channel, create_channel, get_channel_info,
//
//	get_channel_members, add_user_to_channel, get_user_channels
//
// Teams:       get_team_info, get_team_members
// Search:      search_posts, search_users
// Agents:      list_agents
// Automations: list_automations, get_automation_instructions,
//
//	create_automation, update_automation, delete_automation
//
// Keep this map in sync when the agents plugin adds or removes MCP tools.
// Unit tests assert Before handlers are registered only for tools that
// also appear here with Allowed=true.
var mattermostMCPServerTools = map[string]MattermostMCPTool{
	// Posts (mcpserver/tools/posts.go — getPostTools)
	"read_post":     {Allowed: true, TriggererSafe: true, Before: beforeReadPost},
	"create_post":   {Allowed: false},
	"dm":            {Allowed: false},
	"group_message": {Allowed: false},

	// Channels (mcpserver/tools/channels.go — getChannelTools)
	"read_channel":        {Allowed: true, TriggererSafe: true, Before: beforeReadChannel},
	"create_channel":      {Allowed: true, Before: beforeCreateChannel},
	"get_channel_info":    {Allowed: true, TriggererSafe: true, Before: beforeGetChannelInfo},
	"get_channel_members": {Allowed: true, TriggererSafe: true, Before: beforeGetChannelMembers},
	"add_user_to_channel": {Allowed: true, Before: beforeAddUserToChannel},
	"get_user_channels":   {Allowed: true, Before: beforeGetUserChannels},

	// Teams (mcpserver/tools/teams.go — getTeamTools)
	"get_team_info":    {Allowed: true, TriggererSafe: true, Before: beforeGetTeamInfo},
	"get_team_members": {Allowed: true, TriggererSafe: true, Before: beforeGetTeamMembers},

	// Search (mcpserver/tools/search.go — getSearchTools)
	"search_posts": {Allowed: true, TriggererSafe: true, Before: beforeSearchPosts},
	"search_users": {Allowed: true},

	// Agents (mcpserver/tools/agents.go — getAgentTools)
	"list_agents": {Allowed: true},

	// Automations (mcpserver/tools/automations.go — getAutomationTools)
	"list_automations":            {Allowed: false},
	"get_automation_instructions": {Allowed: false},
	"create_automation":           {Allowed: false},
	"update_automation":           {Allowed: false},
	"delete_automation":           {Allowed: false},
}

// BareMattermostMCPToolName strips the embedded Mattermost MCP namespace prefix
// (mattermost__) if present, returning the bare name the catalog is keyed by.
// This is the single place that knows how to normalize a tool name to its
// catalog key; non-Mattermost names are returned unchanged. Callers should pass
// tool names through the catalog accessors below rather than stripping inline.
func BareMattermostMCPToolName(name string) string {
	return strings.TrimPrefix(name, MattermostMCPToolPrefix)
}

// LookupMattermostMCPTool returns the catalog entry for the given tool name,
// accepting either the bare ("read_channel") or namespaced
// ("mattermost__read_channel") form. The second return value is false when the
// tool is not in the catalog.
func LookupMattermostMCPTool(name string) (MattermostMCPTool, bool) {
	entry, ok := mattermostMCPServerTools[BareMattermostMCPToolName(name)]
	return entry, ok
}

// IsAllowedMattermostMCPTool reports whether the given tool name (bare or
// namespaced) is in the catalog (known) and whether automations may include it
// in allowed_tools (allowed). Tools known with Allowed=false are explicitly
// rejected.
func IsAllowedMattermostMCPTool(name string) (known, allowed bool) {
	entry, ok := mattermostMCPServerTools[BareMattermostMCPToolName(name)]
	if !ok {
		return false, false
	}
	return true, entry.Allowed
}

// IsCreatorOnlyMattermostMCPTool reports whether the given tool name is a
// Mattermost MCP tool that may run only as the creator. Catalog tools are
// creator-only by default and become triggerer-usable only when explicitly
// marked TriggererSafe, so a newly added tool fails closed. Unknown/external
// tools return false: they are not Mattermost tools, so this rule does not
// apply to them (and external tools remain the accepted, separate risk).
func IsCreatorOnlyMattermostMCPTool(name string) bool {
	entry, ok := mattermostMCPServerTools[name]
	return ok && !entry.TriggererSafe
}

// IsGuardrailConstrainedMattermostMCPTool reports whether the given tool name
// is a Mattermost MCP tool that channel guardrails actually constrain (its
// catalog entry has a Before hook). Names may be bare or namespaced as
// "mattermost__<tool>"; the prefix is stripped before lookup. External tools
// and unconstrained embedded tools (search_users, list_agents) return false.
func IsGuardrailConstrainedMattermostMCPTool(name string) bool {
	entry, ok := mattermostMCPServerTools[strings.TrimPrefix(name, "mattermost__")]
	return ok && entry.Before != nil
}

// HasGuardrailConstrainedMattermostMCPTool reports whether any entry in
// allowedTools is a guardrail-constrained Mattermost MCP tool.
func HasGuardrailConstrainedMattermostMCPTool(allowedTools []string) bool {
	return slices.ContainsFunc(allowedTools, IsGuardrailConstrainedMattermostMCPTool)
}

// MattermostMCPTools returns a copy of the catalog suitable for iteration in
// tests. Mutating the returned map does not affect the catalog.
func MattermostMCPTools() map[string]MattermostMCPTool {
	out := make(map[string]MattermostMCPTool, len(mattermostMCPServerTools))
	maps.Copy(out, mattermostMCPServerTools)
	return out
}
