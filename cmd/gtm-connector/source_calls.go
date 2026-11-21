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
// and imports never share a workspace: connect calls is refused where a read returned demo call notes, and
// connect demo keeps the imports a workspace holds connected.

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

// attachTranscripts connects imported calls. Their row holds no binding and no identity: imports are bound
// to the CRM. A workspace that read demo call notes is refused, so the two never share one.
func attachTranscripts(ctx context.Context, a attaching) (attached, error) {
	held, err := postgres.CallImports{Store: a.store, WorkspaceID: a.workspace.ID}.HeldOtherCallNotes(ctx)
	if err != nil {
		return attached{}, err
	}
	if held {
		return attached{}, &failure{code: 3, next: "gtm-connector connect --help",
			message: "This workspace has read demo call notes, and imported transcripts never share a workspace " +
				"with them. Connect calls in a workspace of its own with --state-dir NAME. Nothing was changed."}
	}
	return attached{}, nil
}

// keptTranscripts are the kinds connect demo attaches in a workspace: its demo calls become imported ones
// when the workspace's calls are connected to imports, or it holds imports, as the Python package keeps
// the transcripts a folder read beside the demo CRM.
func keptTranscripts(ctx context.Context, store *postgres.Store, workspace postgres.Workspace,
	rows []postgres.Connection, kinds []*sourceKind) ([]*sourceKind, error) {
	imported := kindsNamed("calls")[0]
	keep := false
	for _, row := range rows {
		keep = keep || row.Source == "calls" && kindOf(row.Source, row.Mode) == imported
	}
	if !keep {
		binding, err := postgres.CallImports{Store: store, WorkspaceID: workspace.ID}.Binding(ctx)
		if err != nil {
			return nil, err
		}
		keep = binding != nil
	}
	kept := make([]*sourceKind, len(kinds))
	for i, kind := range kinds {
		kept[i] = kind
		if keep && kind.source == "calls" {
			kept[i] = imported
		}
	}
	return kept, nil
}
