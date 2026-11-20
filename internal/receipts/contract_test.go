package receipts_test

import (
	"testing"

	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/receipts/receiptstest"
)

func TestMemoryKeepsTheContract(t *testing.T) {
	receiptstest.Run(t, func(t *testing.T) *receiptstest.Fixture {
		m := receipts.NewMemory(nil, nil)
		const workspace, principal, account, connection = "ws-1", "p1", "acc-001", "crm"
		generation := int64(1)
		m.SetGeneration(connection, generation)
		return &receiptstest.Fixture{Store: m, Workspace: workspace, Other: "ws-2", Principal: principal,
			Account: account, Connection: connection,
			Bump: func(*testing.T) {
				generation++
				m.SetGeneration(connection, generation)
			},
			Grant: func(*testing.T) { m.Grant(workspace, principal, account) },
			Records: func(t *testing.T, auditID string) []receipts.RecordVersion {
				bound, err := m.Records(workspace, auditID)
				if err != nil {
					t.Fatal(err)
				}
				return bound
			},
			Committed: func(*testing.T) int { return len(m.All()) },
			Published: func(_ *testing.T, briefID string) bool {
				_, found := m.Published(briefID)
				return found
			},
		}
	})
}
