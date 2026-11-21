package main

import (
	"context"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/sources/calls"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// The imported-calls kind: call transcripts the operator imports from local files with debug calls import
// (calls.go), kept by postgres.CallImports and bound to the workspace's CRM. A card reads the active import
// of its account, checked against its stored bytes, and never demo call notes in its place.
//
// Imported calls may sit beside Salesforce or beside the demo CRM, as in the Python package. Demo call notes
// and imports never share a workspace: the database refuses imported calls where a read returned demo call
// notes, and connect demo never makes the calls of a workspace that holds imports demo data.

// transcripts is a workspace's imports as its calls source.
type transcripts struct {
	imports postgres.CallImports
	binding *strictjson.Object
}

func (t transcripts) CallNotes(ctx context.Context, accountID string) ([]*strictjson.Object, error) {
	return calls.CallNotes(ctx, t.imports, t.binding, accountID)
}

// openTranscripts opens the imports of the row's workspace, bound to its CRM as a card records the CRM.
func openTranscripts(_ context.Context, o opening) (callsSource, error) {
	binding, _ := o.crmBinding.(*strictjson.Object)
	if binding == nil {
		return nil, unboundCRM
	}
	if _, bound := evidence.CRMBackend(binding); !bound {
		return nil, unboundCRM
	}
	return transcripts{imports: postgres.CallImports{Store: o.store, WorkspaceID: o.workspace.ID}, binding: binding}, nil
}

// unboundCRM is a CRM that records no binding imports can be bound to.
var unboundCRM = &failure{code: 3, message: "This workspace's CRM records no binding, and imported transcripts " +
	"are read only under one. Nothing was read."}

// demoCallNotesHeld refuses imported calls where a read returned demo call notes. The database makes the
// check as the calls row changes, with the row locked (postgres.ErrDemoCallNotes), so a demo read that
// finishes meanwhile is either seen or refused as changed.
var demoCallNotesHeld = &failure{code: 3, next: "gtm-connector connect --help",
	message: "This workspace has read demo call notes, and imported transcripts never share a workspace with " +
		"them. Connect calls in a workspace of its own with --state-dir NAME. Nothing was changed."}

// attachTranscripts connects imported calls. Their row holds no binding and no identity: imports are bound
// to the CRM.
func attachTranscripts(context.Context, attaching) (attached, error) { return attached{}, nil }

// keptTranscripts are the kinds connect demo attaches in a workspace. Imported calls that are connected
// stay connected. Calls that were detached stay detached when the workspace holds imports: only connect
// calls brings them back, and demo call notes never join them.
func keptTranscripts(ctx context.Context, store *postgres.Store, workspace postgres.Workspace,
	rows []postgres.Connection, kinds []*sourceKind) ([]*sourceKind, error) {
	imported := kindsNamed("calls")[0]
	connected := false
	for _, row := range rows {
		connected = connected || row.Source == "calls" && kindOf(row.Source, row.Mode) == imported
	}
	held := false
	if !connected {
		binding, err := postgres.CallImports{Store: store, WorkspaceID: workspace.ID}.Binding(ctx)
		if err != nil {
			return nil, err
		}
		held = binding != nil
	}
	var kept []*sourceKind
	for _, kind := range kinds {
		switch {
		case kind.source != "calls":
			kept = append(kept, kind)
		case connected:
			kept = append(kept, imported)
		case !held:
			kept = append(kept, kind)
		}
	}
	return kept, nil
}
