package httptransport_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/authz"
	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/storage/postgres/postgrestest"
	httptransport "github.com/pwngh/gtm-connector/internal/transport/http"
)

// refusing is a deal registry that refuses every registration with one error.
type refusing struct {
	err error
	// reason is the reason the last close was given.
	reason string
}

func (r *refusing) RegisterDeal(context.Context, authz.Principal, string) (postgres.Deal, error) {
	return postgres.Deal{}, r.err
}

func (r *refusing) RefreshDeal(context.Context, authz.Principal, string) error { return r.err }

func (r *refusing) CloseDeal(_ context.Context, _ authz.Principal, _, reason string) error {
	r.reason = reason
	return r.err
}

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
		// A close answers the same refusals, since it makes its final card as a refresh makes a card.
		r = b.write(http.MethodPost, "/v1/deals/any-2027-aaaaaa/close", map[string]string{"reason": "Renewed"})
		if r.status != c.status || r.code() != c.code {
			t.Errorf("close, %v: %d %s, want %d %s", c.err, r.status, r.raw, c.status, c.code)
		}
	}
	// A refusal that comes after the card was published says the card stays; one that comes before says none
	// was published. A grant that ended in between answers as any refused grant does.
	for _, c := range []struct {
		err    error
		status int
		code   string
		says   string
	}{
		{postgres.ErrDealChanged, http.StatusConflict, "deal_changed", "No card was published"},
		{fmt.Errorf("%w: %w", httptransport.ErrCardKept, postgres.ErrDealChanged), http.StatusConflict, "deal_changed", "That card stays the deal's"},
		{fmt.Errorf("%w: %w", httptransport.ErrCardKept, errors.New("anything else")), http.StatusInternalServerError, "close_failed", "The card was published and stays"},
		{fmt.Errorf("%w: %w", httptransport.ErrCardKept, authz.ErrNotPermitted), http.StatusForbidden, "not_permitted", "Not permitted."},
	} {
		registry.err = c.err
		for _, path := range []string{"/v1/deals/any-2027-aaaaaa/refresh", "/v1/deals/any-2027-aaaaaa/close"} {
			r := b.write(http.MethodPost, path, map[string]string{"reason": "Renewed"})
			if r.status != c.status || r.code() != c.code || !strings.Contains(string(r.raw), c.says) {
				t.Errorf("%s, %v: %d %s, want %d %s saying %q", path, c.err, r.status, r.raw, c.status, c.code, c.says)
			}
		}
	}
	// A close needs a reason of one line, and gives it to the registry trimmed. One without is refused before
	// the registry is asked: a body that is not the documented object as invalid_json, a reason that is not
	// one line of 1 to 500 characters as invalid_reason.
	registry.err, registry.reason = errors.New("asked"), ""
	if r := b.write(http.MethodPost, "/v1/deals/any-2027-aaaaaa/close", nil); r.status != http.StatusUnsupportedMediaType ||
		registry.reason != "" {
		t.Errorf("close with no body: %d %s", r.status, r.raw)
	}
	for _, c := range []struct {
		body any
		code string
	}{
		{[]string{"Renewed"}, "invalid_json"},
		{"Renewed", "invalid_json"},
		{map[string]any{"reason": 7}, "invalid_json"},
		{map[string]any{"reason": "Renewed", "card": false}, "invalid_json"},
		{map[string]any{"reason": nil}, "invalid_json"},
		{map[string]string{}, "invalid_reason"},
		{map[string]string{"reason": "  "}, "invalid_reason"},
		{map[string]string{"reason": "two\nlines"}, "invalid_reason"},
		{map[string]string{"reason": strings.Repeat("r", 501)}, "invalid_reason"},
	} {
		r := b.write(http.MethodPost, "/v1/deals/any-2027-aaaaaa/close", c.body)
		if r.status != http.StatusBadRequest || r.code() != c.code || registry.reason != "" {
			t.Errorf("close with %v: %d %s, want 400 %s, and the registry was given %q", c.body, r.status, r.raw, c.code, registry.reason)
		}
	}
	if b.write(http.MethodPost, "/v1/deals/any-2027-aaaaaa/close", map[string]string{"reason": " Renewed "}); registry.reason != "Renewed" {
		t.Errorf("the registry was given the reason %q", registry.reason)
	}
}
