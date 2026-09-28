package commands

import (
	"context"
	"fmt"
	"strings"
)

func switchCommand() Definition {
	return Definition{
		Name:        "switch",
		Description: "Switch model",
		SubCommands: []SubCommand{
			{
				Name:        "model",
				Description: "Switch this agent to an exact target or a model route until the next reload",
				ArgsUsage:   "to " + selectionUsage,
				Handler: func(_ context.Context, req Request, rt *Runtime) error {
					if rt == nil || rt.SwitchModel == nil {
						return req.Reply(unavailableMsg)
					}
					// Parse: /switch model to <selection>
					value := nthToken(req.Text, 3) // tokens: [/switch, model, to, <selection>]
					if nthToken(req.Text, 2) != "to" || value == "" {
						return req.Reply("Usage: /switch model to " + selectionUsage)
					}
					previous, err := rt.SwitchModel(value)
					if err != nil {
						return req.Reply(err.Error())
					}
					if strings.TrimSpace(previous) == "" {
						return req.Reply(fmt.Sprintf("Switched model to %s", value))
					}
					return req.Reply(fmt.Sprintf("Switched model from %s to %s", previous, value))
				},
			},
			{
				Name:        "channel",
				Description: "Moved to /check channel",
				Handler: func(_ context.Context, req Request, _ *Runtime) error {
					return req.Reply("This command has moved. Please use: /check channel <name>")
				},
			},
		},
	}
}
