package httptransport_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/pwngh/gtm-connector/internal/authz"
	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/storage/postgres/postgrestest"
	httptransport "github.com/pwngh/gtm-connector/internal/transport/http"
)

// refusing is a deal registry that refuses every registration with one error.
type refusing struct{ err error }

func (r *refusing) RegisterDeal(context.Context, authz.Principal, string) (postgres.Deal, error) {
	return postgres.Deal{}, r.err
}

func (r *refusing) RefreshDeal(context.Context, authz.Principal, string) error { return r.err }

// A deal that is not registered answers the status and code the guide gives for its reason: the CRM
// connected again while it was read is 409 source_changed.
func TestARefusedDealAnswersItsDocumentedCode(t *testing.T) {
	registry := &refusing{}
	s := siteOn(t, postgrestest.New(t), 0, func(c *httptransport.Config) { c.Deals = registry })
	b := s.browser(t)
	b.start()
	if r := b.signIn("demo-reader", readerPassword); r.status != 200 {
		t.Fatalf("sign in: %d %s", r.status, r.raw)
	}
	for _, c := range []struct {
		err    error
		status int
		code   string
	}{
		{receipts.ErrSourceChanged, http.StatusConflict, "source_changed"},
		{postgres.ErrDealOpen, http.StatusConflict, "deal_open"},
		{httptransport.ErrNoCRM, http.StatusConflict, "crm_not_connected"},
		{httptransport.ErrSourceFailed, http.StatusBadGateway, "source_failed"},
		{authz.ErrNotPermitted, http.StatusForbidden, "not_permitted"},
		{errors.New("anything else"), http.StatusInternalServerError, "internal"},
	} {
		registry.err = c.err
		r := b.write(http.MethodPost, "/v1/deals", map[string]string{"account": postgrestest.Account})
		if r.status != c.status || r.code() != c.code {
			t.Errorf("%v: %d %s, want %d %s", c.err, r.status, r.raw, c.status, c.code)
		}
	}
	// A refresh that publishes no card answers its reason too.
	for _, c := range []struct {
		err    error
		status int
		code   string
	}{
		{receipts.ErrSourceChanged, http.StatusConflict, "source_changed"},
		{postgres.ErrDealChanged, http.StatusConflict, "deal_changed"},
		{httptransport.ErrDealClosed, http.StatusConflict, "deal_closed"},
		{httptransport.ErrNoCRM, http.StatusConflict, "crm_not_connected"},
		{httptransport.ErrSourceFailed, http.StatusBadGateway, "source_failed"},
		{httptransport.ErrNoCard, http.StatusUnprocessableEntity, "evidence_refused"},
		{authz.ErrNotPermitted, http.StatusForbidden, "not_permitted"},
		{errors.New("anything else"), http.StatusInternalServerError, "internal"},
	} {
		registry.err = c.err
		r := b.write(http.MethodPost, "/v1/deals/any-2027-aaaaaa/refresh", nil)
		if r.status != c.status || r.code() != c.code {
			t.Errorf("refresh, %v: %d %s, want %d %s", c.err, r.status, r.raw, c.status, c.code)
		}
	}
}
