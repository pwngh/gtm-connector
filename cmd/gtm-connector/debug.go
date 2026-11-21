package main

import (
	"context"
	"fmt"
	"slices"
)

// debug is the Python CLI's group of commands that work on one source by hand. This build runs debug calls
// (calls.go), debug salesforce (source_salesforce.go) and debug slack; each other command of the group is
// refused by name.

// debugCommands are the commands under debug, as the Python CLI lists them.
var debugCommands = []string{"serve", "doctor", "verify", "completion", "salesforce", "calls", "slack"}

// debugInBuild are the commands under debug this build runs, each with its runner.
var debugInBuild = map[string]func(ctx context.Context, tokens []string, s screen) int{
	"calls": runCalls, "salesforce": debugSalesforce, "slack": debugSlack}

const debugHelp = `usage: gtm-connector debug [-h] [--json] COMMAND ...

Serve over HTTPS, check the setup, and work on one source by hand.

positional arguments:
  COMMAND
    calls     Import and inspect private transcript exports without
              contacting a call provider.
    salesforce
              Check Salesforce setup and a renewal brief without printing
              org records.
    slack     Manage Slack authentication, select a thread, or check its
              audited read.

options:
  -h, --help  show this help message and exit
  --json      Write the documented JSON result for scripts.

Not in this build: serve, doctor, verify, completion.

Example: gtm-connector debug slack status
`

// debugCommand runs one command under debug, with --json already taken out.
func debugCommand(ctx context.Context, tokens []string, s screen) int {
	if len(tokens) == 0 {
		return s.fail("debug", refused("the following arguments are required: COMMAND"))
	}
	name := tokens[0]
	switch {
	case name == "-h" || name == "--help":
		fmt.Fprint(s.out, debugHelp)
		return 0
	case debugInBuild[name] != nil:
		return debugInBuild[name](ctx, tokens[1:], s)
	case slices.Contains(debugCommands, name):
		return s.fail("debug", &failure{code: 3, next: "gtm-connector debug --help",
			message: "debug " + name + " is not available in this build."})
	}
	return s.fail("debug", refused("argument COMMAND: invalid choice (choose from %s)", quotedChoices(debugCommands)))
}
