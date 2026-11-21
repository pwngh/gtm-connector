package main

import (
	"context"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// connect is the one command that changes what a workspace reads, as gtm_connector/cli.py's connect does
// for a state folder: it attaches or detaches one source, or shows what is connected. The sources it knows
// are sourceKinds (sources.go). What it saves are connection rows and the seller list, never a credential.
//
// A workspace is made for demo data or for real sources, and keeps that: demo data and a real source never
// share one. So beside the demo CRM, Slack is always demo data and calls are demo data or imported
// transcripts, and beside a real CRM a source is real or not connected.

// assistantApps are the assistant apps whose setup the Python CLI's connect prints. This build prints
// none.
var assistantApps = []string{"inspector", "claude-code", "claude-desktop", "cursor"}

// connectOptions are connect's own options, in the order a refusal lists them.
var connectOptions = []string{"--config-file", "--environment", "--org", "--remove", "--scope", "--seller",
	"--transport"}

// validSellers is whether a list is one a workspace may hold as its sellers: at most 200 names or Slack
// IDs, each of 1 to 200 characters.
func validSellers(sellers []string) bool {
	for _, seller := range sellers {
		if n := utf8.RuneCountInString(seller); n < 1 || n > 200 || !utf8.ValidString(seller) {
			return false
		}
	}
	return len(sellers) <= 200
}

// connectedSources are the sources that are connected, in the order of their rows.
func connectedSources(rows []postgres.Connection) []string {
	var sources []string
	for _, row := range rows {
		if row.Mode != "none" {
			sources = append(sources, row.Source)
		}
	}
	return sources
}

// shownSources is what is connected as connect's results show it: each connected source with its provider
// and what its binding holds. The sources named in first come first, then the others, the CRM before its
// calls and Slack.
func shownSources(rows []postgres.Connection, first []string) (*strictjson.Object, error) {
	held := map[string]postgres.Connection{}
	for _, row := range rows {
		if row.Mode != "none" {
			held[row.Source] = row
		}
	}
	sources := &strictjson.Object{}
	for _, source := range append(slices.Clone(first), sourceNames...) {
		row, has := held[source]
		if _, shown := sources.Get(source); !has || shown {
			continue
		}
		provider := row.Mode
		if kind := kindOf(source, row.Mode); kind != nil {
			provider = kind.shown
		}
		entry := strictjson.NewObject("provider", provider)
		binding, err := bindingOf(row)
		if err != nil {
			return nil, err
		}
		for i, key := range binding.Keys {
			if key != "provider" {
				entry.Set(key, binding.Values[i])
			}
		}
		sources.Set(source, entry)
	}
	return sources, nil
}

// connect attaches or detaches one source, or shows what the workspace is connected to.
func (r *run) connect(ctx context.Context) (any, error) {
	name := "status"
	if len(r.args.positionals) > 0 {
		name = r.args.positionals[0]
	}
	kinds, app := kindsNamed(name), slices.Contains(assistantApps, name)
	var taken []string
	switch {
	case app:
		taken = []string{"--scope", "--transport", "--config-file"}
	case name != "status":
		taken = append([]string{"--remove"}, kinds[0].options...)
	}
	var given, stray []string
	for _, option := range connectOptions {
		if _, has := r.args.text(option); has {
			given = append(given, option)
			if !slices.Contains(taken, option) {
				stray = append(stray, option)
			}
		}
	}
	if len(stray) > 0 {
		return nil, refused("connect %s does not take %s.", name, strings.Join(stray, ", "))
	}
	_, remove := r.args.text("--remove")
	if remove && len(given) > 1 {
		return nil, refused("--remove detaches a source and takes no other option.")
	}
	if app {
		return nil, &failure{code: 3, message: "connect " + name + " is not available in this build. " +
			"No setup was printed and nothing was granted."}
	}
	if name == "status" {
		return r.connected(ctx)
	}
	result := func(status string, rows []postgres.Connection, first []string) (any, error) {
		sources, err := shownSources(rows, first)
		return strictjson.NewObject("schema_version", 1, "provider", name, "status", status, "sources", sources), err
	}
	source := kinds[0].source
	if remove {
		if err := r.open(ctx); err != nil {
			return nil, err
		}
		if !r.exists {
			return result("not_connected", nil, nil)
		}
		status := "not_connected"
		rows, err := r.store.UpdateSources(ctx, r.workspace, func(current []postgres.Connection) (postgres.SourceUpdate, error) {
			var update postgres.SourceUpdate
			for _, row := range current {
				if kind := kindOf(row.Source, row.Mode); row.Source == source && kind != nil && kind.provider == name {
					status = "removed"
				}
			}
			for _, row := range current {
				// Calls and Slack belong to their CRM, so removing it removes them.
				if status == "removed" && row.Mode != "none" && (row.Source == source || source == "crm") {
					update.Changes = append(update.Changes, postgres.SourceChange{Source: row.Source, Mode: "none"})
				}
			}
			return update, nil
		})
		if err != nil {
			return nil, err
		}
		return result(status, rows, connectedSources(rows))
	}
	sellers, listed := r.args.lists["--seller"]
	if listed && !validSellers(sellers) {
		return nil, refused("Name at most 200 sellers with --seller, each a name or Slack ID of 1 to 200 characters.")
	}
	if kinds[0].attach == nil {
		return nil, &failure{code: 3, message: "connect " + name + " is not available in this build. " +
			"Nothing was connected, and demo data is never read in its place."}
	}
	if err := r.open(ctx); err != nil {
		return nil, err
	}
	before, after, err := r.attach(ctx, kinds, sellers)
	if err != nil {
		return nil, err
	}
	// The result lists the sources as the Python CLI's does: a CRM connected where it was not starts the
	// list, and a source connected again keeps the place it had.
	first := connectedSources(before)
	if source == "crm" {
		first = nil
		for _, row := range before {
			if kind := kindOf("crm", row.Mode); row.Source == "crm" && kind != nil && kind.provider == name {
				first = connectedSources(before)
			}
		}
	}
	return result("connected", after, first)
}

// connected is connect status: the workspace's connected sources and its seller list. A workspace that
// does not exist has neither.
func (r *run) connected(ctx context.Context) (any, error) {
	if err := r.open(ctx); err != nil {
		return nil, err
	}
	var rows []postgres.Connection
	sellers := []any{}
	if r.exists {
		var err error
		if rows, err = r.store.Connections(ctx, r.workspace.ID); err != nil {
			return nil, err
		}
		saved, err := r.store.Sellers(ctx, r.workspace.ID)
		if err != nil {
			return nil, err
		}
		for _, seller := range saved {
			sellers = append(sellers, seller)
		}
	}
	sources, err := shownSources(rows, connectedSources(rows))
	return strictjson.NewObject("schema_version", 1, "sources", sources, "sellers", sellers), err
}

// pinnedElsewhere refuses a source whose identity is not the one its workspace is pinned to.
var pinnedElsewhere = &failure{code: 3, next: "gtm-connector connect --help",
	message: "This workspace belongs to a different connection of this source, and keeps the accounts, cards and " +
		"receipts read through it. Connect the one it was first connected to, or connect this one in a workspace " +
		"of its own with --state-dir NAME. Nothing was changed."}

// unpinned refuses a CRM with an identity in a workspace that was connected to a CRM with none.
var unpinned = &failure{code: 3, next: "gtm-connector connect --help",
	message: "This workspace has no pinned connection of its CRM, so what it holds may belong to another. " +
		"Connect this one in a workspace of its own with --state-dir NAME. Nothing was changed."}

// attach connects the run's workspace to the kinds one name of connect stands for, and saves the seller
// list when one is given. A workspace that does not exist is made, for demo data or for real sources as
// the kinds are. Each kind checks what was asked before anything is saved, and the rows are saved together
// or not at all. It returns the workspace's connections as they were and as they are.
//
// A source's row keeps the identity it was first connected to, also while it is detached, and an identity
// that is not that one is refused here for every kind. A CRM with an identity is refused in a workspace
// whose CRM row has no pin, as the Python package refuses a state folder with no pinned connection. So
// calls and Slack, which belong to their CRM, are only ever kept beside the CRM they were connected to.
func (r *run) attach(ctx context.Context, kinds []*sourceKind, sellers []string) (before, after []postgres.Connection, err error) {
	holds := "demo"
	switch {
	case kinds[0].besideDemo && r.exists:
		holds = r.workspace.Kind
	case kinds[0].live:
		holds = "live"
	}
	switch {
	case r.exists && r.workspace.Kind != holds && holds == "demo":
		return nil, nil, &failure{code: 3, message: "This workspace reads real sources, and demo data never shares " +
			"a workspace with them. Connect the demo data in a workspace of its own with --state-dir NAME."}
	case r.exists && r.workspace.Kind != holds:
		return nil, nil, &failure{code: 3, message: "This workspace holds demo data, and a real source never shares " +
			"a workspace with it. Connect " + kinds[0].provider + " in a workspace of its own with --state-dir NAME."}
	case r.exists:
		if before, err = r.store.Connections(ctx, r.workspace.ID); err != nil {
			return nil, nil, err
		}
		if kinds[0].provider == "demo" {
			if kinds, err = keptTranscripts(ctx, r.store, r.workspace, before, kinds); err != nil {
				return nil, nil, err
			}
		}
	}
	var crm *postgres.Connection
	for i, row := range before {
		if row.Source == "crm" && row.Mode != "none" {
			crm = &before[i]
		}
	}
	if kinds[0].source != "crm" && crm == nil {
		return nil, nil, errNotConnected
	}
	var changes []postgres.SourceChange
	for _, kind := range kinds {
		pinned, present := "", false
		for _, row := range before {
			if row.Source == kind.source {
				pinned, present = row.IdentitySHA256, true
			}
		}
		saved, err := kind.attach(ctx, attaching{args: r.args, store: r.store, workspace: r.workspace, crm: crm,
			pinned: pinned})
		if err != nil {
			return nil, nil, err
		}
		switch {
		case pinned != "" && saved.identity != pinned:
			return nil, nil, pinnedElsewhere
		case kind.source == "crm" && present && pinned == "" && saved.identity != "":
			// The workspace was read through a CRM with no pin, so this one is not known to be it.
			return nil, nil, unpinned
		}
		change := postgres.SourceChange{Source: kind.source, Mode: kind.mode, IdentitySHA256: saved.identity}
		if saved.binding != nil {
			if change.Binding, err = strictjson.Dumps(saved.binding, strictjson.Prepared); err != nil {
				return nil, nil, err
			}
		}
		changes = append(changes, change)
	}
	if !r.exists {
		// A name given with --state-dir can be a path or hold a secret, so the workspace is named by its slug.
		slug := workspaceSlug(r.name)
		if r.workspace, err = r.store.EnsureWorkspace(ctx, slug, holds, "Workspace "+slug); err != nil {
			return nil, nil, err
		}
		r.exists = true
	}
	after, err = r.store.UpdateSources(ctx, r.workspace, func(current []postgres.Connection) (postgres.SourceUpdate, error) {
		if !slices.Equal(current, before) {
			return postgres.SourceUpdate{}, local("This workspace's sources changed while connect ran. Nothing was " +
				"saved. Run the command again.")
		}
		return postgres.SourceUpdate{Changes: changes, Sellers: sellers}, nil
	})
	return before, after, err
}
