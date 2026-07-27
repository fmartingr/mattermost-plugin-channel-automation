package automation

import (
	"fmt"

	"github.com/mattermost/mattermost-plugin-channel-automation/server/model"
)

// ActionError is returned by Executor.Execute when a specific action
// fails. It carries the failing action's ID and type so callers can surface
// targeted error notifications without parsing the wrapped error string.
type ActionError struct {
	ActionID   string
	ActionType string
	Err        error
}

func (e *ActionError) Error() string {
	return fmt.Sprintf("action %q failed: %s", e.ActionID, e.Err)
}

func (e *ActionError) Unwrap() error { return e.Err }

// Executor dispatches automation actions using the registry.
type Executor struct {
	registry *Registry
}

// NewExecutor creates an Executor with the given registry.
func NewExecutor(registry *Registry) *Executor {
	return &Executor{registry: registry}
}

// Execute runs all actions in the automation sequentially, building up the AutomationContext.
// Returns the context (with any partial step outputs) and an error on the first
// failure or if an action type is unknown.
func (e *Executor) Execute(f *model.Automation, triggerData model.TriggerData) (*model.AutomationContext, error) {
	ctx := &model.AutomationContext{
		AutomationID: f.ID,
		CreatedBy:    f.CreatedBy,
		TriggerType:  f.Trigger.Type(),
		Trigger:      triggerData,
		Steps:        make(map[string]model.StepOutput),
		Actions:      f.Actions,
	}

	for _, action := range f.Actions {
		handler, ok := e.registry.GetAction(action.Type())
		if !ok {
			return ctx, fmt.Errorf("unknown action type %q for action %q", action.Type(), action.ID)
		}

		output, err := handler.Execute(&action, ctx)
		if err != nil {
			return ctx, &ActionError{
				ActionID:   action.ID,
				ActionType: action.Type(),
				Err:        err,
			}
		}

		if output != nil {
			ctx.Steps[action.ID] = *output
		}
	}

	return ctx, nil
}
