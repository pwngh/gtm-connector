package receipts_test

import (
	"testing"

	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/receipts/receiptstest"
)

func TestMemoryKeepsTheContract(t *testing.T) {
	receiptstest.Run(t, func(t *testing.T) *receiptstest.Fixture {
		m := receipts.NewMemory(nil, nil)
		const workspace, principal, account = "ws-1", "p1", "acc-001"
		generation := int64(1)
		m.SetGeneration(workspace, "crm", generation)
		m.SetGeneration("ws-2", "crm-other", 1)
		return &receiptstest.Fixture{Store: m, Workspace: workspace, Other: "ws-2", Principal: principal,
			Stranger: "p2", Account: account, OtherAccount: "acc-002", Connection: "crm",
			OtherConnection: "crm-other",
			Bump: func(*testing.T) {
				generation++
				m.SetGeneration(workspace, "crm", generation)
			},
			Grant:     func(*testing.T) { m.Grant(workspace, principal, account) },
			Committed: func(*testing.T) int { return len(m.All()) },
			Published: func(_ *testing.T, briefID string) bool {
				_, found := m.Published(workspace, briefID)
				return found
			},
		}
	})
}
