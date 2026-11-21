package people_test

import (
	"testing"

	p "github.com/pwngh/gtm-connector/internal/people"
)

// A participant name is never compared with a label on its own: only rung 5 reads it, for a person the
// call's attendee list names by email.
func TestAParticipantNameNeedsTheAttendeeList(t *testing.T) {
	k := p.Known{Account: "acc-1", NameVariants: true, People: []p.Person{
		{Key: "contact:c1", Identifiers: []p.Identifier{{Kind: p.SalesforceContact, Value: "c1", Scope: "acc-1"},
			{Kind: p.Name, Value: "Dana Crest"}}},
		{Key: "user:u1", Identifiers: []p.Identifier{{Kind: p.SalesforceUser, Value: "u1"},
			{Kind: p.ParticipantName, Value: "David Lee"}, {Kind: p.Email, Value: "dlee@seller.example"}}}}}
	for _, source := range []p.Source{p.Call, p.Slack} {
		if r := p.Resolve(k, p.Ref{Source: source, CallID: "x", Label: "David Lee"}); r.Role != p.Unknown || r.Person != nil {
			t.Errorf("%s: a bare label is %s by rung %d", source, r.Role, r.Rung)
		}
	}
	r := p.Resolve(k, p.Ref{Source: p.Call, CallID: "x", Label: "David Lee",
		Attendees: []p.Attendee{{Label: "Host", Email: "dlee@seller.example"}}})
	if r.Role != p.Seller || r.Rung != p.RungVariant {
		t.Errorf("a known participant is %s by rung %d", r.Role, r.Rung)
	}
}

// A member of the seller's own Slack workspace is never put on the customer side by an email's domain.
func TestAHomeMemberIsNoCustomerByDomain(t *testing.T) {
	k := p.Known{Account: "acc-1", AccountDomains: []string{"buyer.example"}, HomeTeam: "T1"}
	home := p.Resolve(k, p.Ref{Source: p.Slack, Label: "U1", Slack: &p.SlackAuthor{Team: "T1", User: "U1",
		Email: "kim@buyer.example"}})
	guest := p.Resolve(k, p.Ref{Source: p.Slack, Label: "U2", Slack: &p.SlackAuthor{Team: "T2", User: "U2",
		Email: "kim@buyer.example"}})
	if home.Role == p.CustomerNotInCRM || guest.Role != p.CustomerNotInCRM {
		t.Errorf("home member %s, guest %s", home.Role, guest.Role)
	}
}
