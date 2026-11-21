package mcptools

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/strictjson"
	"github.com/pwngh/gtm-connector/internal/transport/mcp"
)

// punctual is a receipt store that refuses a start whose context has already ended, as PostgreSQL does.
type punctual struct{ receipts.Store }

func (p punctual) Start(ctx context.Context, s receipts.Start) (receipts.Receipt, error) {
	if err := ctx.Err(); err != nil {
		return receipts.Receipt{}, err
	}
	return p.Store.Start(ctx, s)
}

// Opening the sources may take all the time it is given and the receipt is still written, in the half of the
// start the recorder keeps for it: the call is refused under its receipt, not left without one.
func TestSlowSourcesLeaveTheReceiptItsTime(t *testing.T) {
	r, store := newRecorder(t)
	r.store = punctual{store}
	r.sources = func(ctx context.Context) (Opened, error) {
		select {
		case <-ctx.Done():
			return Opened{}, ctx.Err()
		case <-time.After(time.Minute):
			return Opened{}, errors.New("opening the sources was given no deadline")
		}
	}
	starting, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	known := knownBearer
	id, err := r.Start(starting, mcp.Start{Token: &known, Tool: "get_account",
		Arguments: strictjson.NewObject("account_id", "acc-003")})
	if err != nil {
		t.Fatalf("no receipt: %v", err)
	}
	_, refusal := r.opened(id)
	if receipt := stored(t, store, id); receipt.Status != "pending" || receipt.PrincipalID != "principal-1" ||
		!errors.Is(refusal, context.DeadlineExceeded) {
		t.Fatalf("the receipt %+v, refusal %v", receipt, refusal)
	}
}
