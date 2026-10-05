package commands

// /approve and /deny answer an approval request Compa posted (a tool call
// tools.approval says to ask about, with no approver registered). The agent
// acts on them as soon as they arrive, before any command runs, and counts
// only the owner's. These definitions list them in /help and command menus;
// having no handler, the executor passes them through instead of handling
// them twice.

func approveCommand() Definition {
	return Definition{
		Name:        "approve",
		Description: "Approve a request Compa is waiting on",
		Usage:       "/approve <id>",
	}
}

func denyCommand() Definition {
	return Definition{
		Name:        "deny",
		Description: "Deny a request Compa is waiting on",
		Usage:       "/deny <id>",
	}
}
