package httptransport_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/authz"
	"github.com/pwngh/gtm-connector/internal/render"
	"github.com/pwngh/gtm-connector/internal/render/webhook"
)

// A unit number in a webhook body built from a published card resolves through the API link the body
// carries, for a person granted the account, and is refused to a person who is not.
func TestAWebhookUnitResolvesThroughTheAPI(t *testing.T) {
	s := newSite(t, 0)
	view, err := authz.Evidence{Store: s.Store}.Card(t.Context(),
		authz.Principal{WorkspaceID: s.Workspace.ID, ID: s.reader.ID}, s.Card.ID)
	if err != nil {
		t.Fatal(err)
	}
	cited := len(view.Units)
	body, err := webhook.Body(webhook.Delivery{ID: "evt_1", Type: webhook.RiskChanged, OccurredAt: time.Now()},
		render.Event{Deal: render.Deal{ID: view.Card.AccountRef, Status: render.AtRisk, Previous: render.OnTrack},
			Card: view, Rules: []render.Rule{{Code: "close_date_slippage", State: render.Raised, Previous: render.Clear,
				Sentence: "The close date moved 2 times in 30 days.", Cites: []int{cited}}}},
		render.Site{Address: s.origin, Revision: "main"})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Rules []struct{ Units []int }
		Links struct{ API string }
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	path, found := strings.CutPrefix(got.Links.API, s.origin)
	if !found || len(got.Rules) != 1 || len(got.Rules[0].Units) != 1 {
		t.Fatalf("body:\n%s", body)
	}
	path += fmt.Sprintf("/units/%d", got.Rules[0].Units[0])
	reader := s.browser(t)
	reader.signIn("demo-reader", readerPassword)
	if r := reader.do(http.MethodGet, path, nil, nil); r.status != 200 || r.body["n"] != float64(cited) {
		t.Fatalf("%s: %d %s", path, r.status, r.raw)
	}
	outsider := s.browser(t)
	outsider.signIn("demo-outsider", outsiderPassword)
	if r := outsider.do(http.MethodGet, path, nil, nil); r.status != 403 {
		t.Errorf("outsider %s: %d", path, r.status)
	}
}
