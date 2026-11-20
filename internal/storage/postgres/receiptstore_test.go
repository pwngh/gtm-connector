package postgres_test

import (
	"testing"

	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/receipts/receiptstest"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/storage/postgres/pgtest"
	"github.com/pwngh/gtm-connector/internal/storage/postgres/postgrestest"
)

func TestReceiptStoreKeepsTheContract(t *testing.T) {
	receiptstest.Run(t, func(t *testing.T) *receiptstest.Fixture {
		url := pgtest.URL(t)
		store, err := postgres.Open(ctx, url)
		ok(t, err)
		t.Cleanup(store.Close)
		ok(t, store.Migrate(ctx))
		workspace, err := store.EnsureWorkspace(ctx, "contract", "demo", "Contract workspace")
		ok(t, err)
		other, err := store.EnsureWorkspace(ctx, "elsewhere", "demo", "Another workspace")
		ok(t, err)
		principal, err := store.CreatePrincipal(ctx, workspace, "person", "Demo reader")
		ok(t, err)
		const account = "acc-001"
		ok(t, store.EnsureAccount(ctx, workspace.ID, account, "Invented account"))
		connection, err := store.EnsureConnection(ctx, workspace, "crm", "demo")
		ok(t, err)
		f := &postgrestest.Fixture{URL: url} // for count
		return &receiptstest.Fixture{Store: &postgres.ReceiptStore{Store: store}, Workspace: workspace.ID,
			Other: other.ID, Principal: principal.ID, Account: account, Connection: connection.ID,
			Bump: func(t *testing.T) {
				_, err := store.BumpConnectionGeneration(ctx, connection.ID)
				ok(t, err)
			},
			Grant: func(t *testing.T) { ok(t, store.GrantAccount(ctx, workspace.ID, principal.ID, account)) },
			Records: func(t *testing.T, auditID string) []receipts.RecordVersion {
				bound, err := store.ReceiptRecords(ctx, workspace.ID, auditID)
				ok(t, err)
				out := make([]receipts.RecordVersion, len(bound))
				for i, v := range bound {
					out[i] = receipts.RecordVersion{Source: v.Source, Table: v.Table, RecordID: v.RecordID,
						ContentVersion: v.ContentVersion, Body: v.Body}
				}
				return out
			},
			Committed: func(t *testing.T) int { return count(t, f, "SELECT count(*) FROM receipts") },
			Published: func(t *testing.T, briefID string) bool {
				return count(t, f, "SELECT count(*) FROM cards WHERE brief_id = $1", briefID) == 1
			},
		}
	})
}
