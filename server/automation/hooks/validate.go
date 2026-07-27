package hooks

import (
	"errors"
	"fmt"
	"strings"

	"github.com/mattermost/mattermost-plugin-agents/public/bridgeclient"

	"github.com/mattermost/mattermost-plugin-channel-automation/server/model"
)

// ErrToolDiscovery wraps failures to discover the agent's tool catalog
// (bridge unavailable, HTTP error, etc.). These are runtime/dependency
// failures rather than client validation errors, so callers should map
// them to a 5xx response instead of 400.
var ErrToolDiscovery = errors.New("tool discovery failed")

// AgentToolsLister fetches the set of tools an agent exposes for a given user
// from the Mattermost agents bridge. Implemented by *bridgeclient.Client; kept
// as an interface so the validator can be unit-tested without live HTTP.
type AgentToolsLister interface {
	GetAgentTools(agentID, userID string) ([]bridgeclient.BridgeToolInfo, error)
}

// ValidateAllowedTools verifies, for every ai_prompt action targeting an
// agent provider, that userID has bridge-level access to that agent. When
// the action also specifies allowed_tools, each entry must be genuinely
// available to userID via that agent, and any embedded Mattermost MCP tool
// must be present in the supported catalog with Allowed=true.
//
// The bridge is queried at most once per distinct provider_id within an automation,
// so the access check and tool-name validation share a single call.
// Returns nil for automations with no ai_prompt agent actions.
func ValidateAllowedTools(f *model.Automation, userID string, bridge AgentToolsLister) error {
	if f == nil {
		return nil
	}

	// Cache results per agent so a automation with multiple ai_prompt actions
	// pointing at the same agent only triggers one bridge call. This also
	// dedupes the access check against any tool-name validation below.
	cache := make(map[string]map[string]bridgeclient.BridgeToolInfo)

	for i, a := range f.Actions {
		if a.AIPrompt == nil || a.AIPrompt.ProviderType != model.AIProviderTypeAgent {
			continue
		}
		if userID == "" {
			return fmt.Errorf("action %d: cannot verify agent access: missing user id", i)
		}
		agentID := a.AIPrompt.ProviderID
		if agentID == "" {
			return fmt.Errorf("action %d: ai_prompt with provider_type %q requires provider_id", i, model.AIProviderTypeAgent)
		}

		available, ok := cache[agentID]
		if !ok {
			if bridge == nil {
				return errors.Join(fmt.Errorf("action %d: cannot verify access to agent %q: bridge client unavailable", i, agentID), ErrToolDiscovery)
			}
			// A successful (nil-error) GetAgentTools response means the bridge
			// confirmed userID has access to agentID. The tools slice may be
			// empty for agents with DisableTools or no allowlistable tools;
			// that is not a failure. A non-nil error covers both "agent not
			// found" (404) and "permission denied" (403).
			tools, err := bridge.GetAgentTools(agentID, userID)
			if err != nil {
				return errors.Join(fmt.Errorf("action %d: failed to list tools for agent %q: %w", i, agentID, err), ErrToolDiscovery)
			}
			available = make(map[string]bridgeclient.BridgeToolInfo, len(tools))
			for _, t := range tools {
				// Index by both the namespaced Name and the legacy BareName so a
				// stored allowlist entry matching either form resolves. BareName
				// is absent on older agents releases, which already return bare
				// names in Name. Bare names can collide across MCP servers;
				// last-write-wins is acceptable since the bridge resolves an
				// ambiguous bare allowlist entry at completion time.
				available[t.Name] = t
				if t.BareName != "" && t.BareName != t.Name {
					available[t.BareName] = t
				}
			}
			cache[agentID] = available
		}

		if len(a.AIPrompt.AllowedTools) == 0 {
			continue
		}

		hasGuardrails := a.AIPrompt.Guardrails != nil && len(a.AIPrompt.Guardrails.Channels) > 0
		for _, name := range a.AIPrompt.AllowedTools {
			info, ok := available[name]
			if !ok {
				return fmt.Errorf("action %d: tool %q is not available to the automation owner for agent %q", i, name, agentID)
			}
			if info.ServerOrigin != EmbeddedMattermostMCPOrigin {
				continue
			}
			// The catalog is keyed by bare tool names, so compare against the
			// resolved tool's bare name. Old agents releases omit BareName but
			// already return bare names in Name, so fall back to it.
			bare := info.BareName
			if bare == "" {
				bare = info.Name
			}
			known, allowed := IsAllowedMattermostMCPTool(bare)
			if !known {
				return fmt.Errorf("action %d: Mattermost MCP tool %q is not in the supported catalog", i, name)
			}
			if !allowed {
				return fmt.Errorf("action %d: Mattermost MCP tool %q is not permitted in automations", i, name)
			}
			if hasGuardrails && bare == "get_user_channels" {
				return fmt.Errorf("action %d: get_user_channels is not permitted when channel guardrails are configured", i)
			}
		}
	}
	return nil
}

// CheckCreatorOnlyTools returns an error when allowedTools contains a
// creator-only tool (a write/enumeration tool that borrows the acting user's
// authority or cannot be constrained by channel guardrails) while the
// (triggerType, requestAs) combination could run as the triggerer. The
// creator-vs-triggerer decision lives entirely in model.ResolvesToTriggerer so
// the rule is defined once; this function only maps it onto the tool list.
func CheckCreatorOnlyTools(triggerType, requestAs string, allowedTools []string) error {
	if !model.ResolvesToTriggerer(triggerType, requestAs) {
		return nil
	}
	for _, name := range allowedTools {
		bare := strings.TrimPrefix(name, "mattermost__")
		if IsCreatorOnlyMattermostMCPTool(bare) {
			return fmt.Errorf("tool %q requires request_as %q (it borrows write authority or cannot be constrained by channel guardrails)", name, model.AIPromptRequestAsCreator)
		}
	}
	return nil
}

// ValidateCreatorOnlyToolsForTriggerer applies CheckCreatorOnlyTools to every
// ai_prompt action in the automation, used at create/update time.
func ValidateCreatorOnlyToolsForTriggerer(f *model.Automation) error {
	if f == nil {
		return nil
	}
	triggerType := f.Trigger.Type()
	for i := range f.Actions {
		ai := f.Actions[i].AIPrompt
		if ai == nil {
			continue
		}
		if err := CheckCreatorOnlyTools(triggerType, ai.RequestAs, ai.AllowedTools); err != nil {
			return fmt.Errorf("action %d: %w", i, err)
		}
	}
	return nil
}
