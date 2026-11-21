package people

import "sort"

// Resolve decides who a speaker reference is and which side they are on. It reads nothing but its
// arguments, so the same arguments always give the same result, whatever order the lists are in.
func Resolve(k Known, r Ref) Result {
	w := newWorld(k)
	label := Label(r.Label)
	anon := label != "" && Anonymous(label)
	rungs := [...]func() finding{
		func() finding { return w.platform(r) },
		func() finding { return w.attendees(r, label, anon) },
		func() finding { return w.alias(r, label, anon) },
		func() finding { return w.exact(r, label, anon) },
		func() finding { return w.variant(r, label, anon) },
	}
	var pending *finding
	shared := false
	for i, run := range rungs {
		f := run()
		f.rung = Rung(i + 1)
		shared = shared || f.shared
		switch {
		case f.reason != NoReason || len(f.people) > 1:
			// The rung found more than one person or side. A side a stronger rung already gave stands;
			// otherwise nothing is picked.
			if pending != nil {
				return w.side(*pending)
			}
			if f.reason == NoReason {
				f.reason = SeveralCandidates
				if w.bothSides(f.people) {
					f.reason = CandidatesOnBothSides
				}
			}
			return w.unknown(f, f.reason, f.people)
		case len(f.people) == 1:
			p := f.people[0]
			role, why := w.role(p)
			// Under a side a stronger rung gave, the person must be on it. A person whose own side is
			// unknown, or whose email contradicts it, adds nothing to it.
			if pending != nil && (role == Unknown || f.conflict[p]) {
				return w.side(*pending)
			}
			if pending != nil && class(role) != class(pending.side) {
				return w.unknown(f, RungsDisagree, f.people)
			}
			if f.conflict[p] {
				return w.unknown(f, EmailDomainDisagrees, f.people)
			}
			return Result{Person: &w.people[p], Role: role, Rung: f.rung, Basis: f.basis, Reason: why,
				Matched: f.matched[p], Rules: RulesVersion}
		case f.side != "":
			if pending == nil {
				pending = &f
			} else if class(f.side) != class(pending.side) {
				return w.unknown(f, RungsDisagree, nil)
			}
		}
	}
	if pending != nil {
		return w.side(*pending)
	}
	reason := NoMatch
	switch {
	case shared:
		reason = SharedDomain
	case anon:
		reason = AnonymousLabel
	case label == "":
		reason = NoLabel
	case r.Source == Slack:
		reason = SlackAuthorUnidentified
	}
	return Result{Role: Unknown, Basis: NoBasis, Reason: reason, Rules: RulesVersion}
}

// finding is what one rung found: people, or a side with no person, or a reason it cannot decide.
type finding struct {
	rung     Rung
	basis    Basis
	people   []int // indices into the people, ascending
	matched  map[int]Identifier
	conflict map[int]bool // the person an email named is on the side the email's domain contradicts
	side     Role         // a side with no person: by an email's domain, or by a room
	ref      Identifier   // the reference's email a side by domain rests on
	domain   string
	reason   Reason // set when the rung cannot decide: a room on mixed sides, or emails on two sides
	shared   bool   // an email on a shared domain gave nothing
}

func (f *finding) add(i int, id Identifier) {
	if f.matched == nil {
		f.matched = map[int]Identifier{}
	}
	old, seen := f.matched[i]
	if !seen {
		f.people = append(f.people, i)
		sort.Ints(f.people)
	}
	if !seen || before(id, old) {
		f.matched[i] = id
	}
}

// before orders identifiers so that the one a result reports does not depend on input order.
func before(a, b Identifier) bool {
	if a.Kind != b.Kind {
		return a.Kind < b.Kind
	}
	if a.Value != b.Value {
		return a.Value < b.Value
	}
	if a.Scope != b.Scope {
		return a.Scope < b.Scope
	}
	return a.From < b.From
}

// world is Known indexed for lookups.
type world struct {
	k             Known
	people        []Person
	accounts      map[string]bool // the deal's account and its related accounts
	company       map[string]bool
	account       map[string]bool
	shared        map[string]bool
	sellerFact    []bool
	customerFact  []bool
	unrelatedFact []bool
}

func newWorld(k Known) *world {
	w := &world{k: k, people: k.People, accounts: map[string]bool{}, company: map[string]bool{},
		account: map[string]bool{}, shared: map[string]bool{}}
	if k.Account != "" {
		w.accounts[k.Account] = true
	}
	for _, a := range k.Related {
		if a != "" {
			w.accounts[a] = true
		}
	}
	for _, d := range k.CompanyDomains {
		w.company[domain(d)] = true
	}
	for _, d := range k.AccountDomains {
		w.account[domain(d)] = true
	}
	for _, d := range k.SharedDomains {
		w.shared[domain(d)] = true
	}
	// A domain on both the seller's list and the account's shows no side.
	for d := range w.company {
		if w.account[d] {
			w.shared[d] = true
		}
	}
	n := len(k.People)
	w.sellerFact, w.customerFact, w.unrelatedFact = make([]bool, n), make([]bool, n), make([]bool, n)
	for i, p := range k.People {
		for _, id := range p.Identifiers {
			switch id.Kind {
			case SalesforceUser, ListedSeller:
				w.sellerFact[i] = true
			case SalesforceContact:
				if w.accounts[id.Scope] {
					w.customerFact[i] = true
				} else {
					w.unrelatedFact[i] = true
				}
			}
		}
	}
	return w
}

// role is the side a person is on, from their records alone.
func (w *world) role(i int) (Role, Reason) {
	switch {
	case w.sellerFact[i] && w.customerFact[i]:
		return Unknown, PersonOnBothSides
	case w.sellerFact[i]:
		return Seller, NoReason
	case w.customerFact[i]:
		return Customer, NoReason
	case w.unrelatedFact[i]:
		return Unknown, AccountNotRelated
	}
	return Unknown, PersonWithoutSide
}

func (w *world) bothSides(people []int) bool {
	seller, customer := false, false
	for _, i := range people {
		seller = seller || w.sellerFact[i]
		customer = customer || w.customerFact[i]
	}
	return seller && customer
}

// class groups roles into sides: a customer and a customer-side speaker not in the CRM are one side.
func class(r Role) Role {
	if r == CustomerNotInCRM {
		return Customer
	}
	return r
}

func (w *world) keys(people []int) []string {
	if len(people) == 0 {
		return nil
	}
	out := make([]string, len(people))
	for i, p := range people {
		out[i] = w.people[p].Key
	}
	sort.Strings(out)
	return out
}

func (w *world) unknown(f finding, reason Reason, people []int) Result {
	return Result{Role: Unknown, Rung: f.rung, Basis: f.basis, Reason: reason, Candidates: w.keys(people),
		Rules: RulesVersion}
}

func (w *world) side(f finding) Result {
	return Result{Role: f.side, Rung: f.rung, Basis: f.basis, Matched: f.ref, Domain: f.domain, Rules: RulesVersion}
}

// domainSide is the side an email domain shows: the seller's own domain (only where home holds, the
// seller's own workspace or a meeting), the account's domain, or a third organisation. A free or shared
// domain shows none.
func (w *world) domainSide(d string, home bool) (Role, bool) {
	switch {
	case w.shared[d]:
		return "", false
	case w.company[d]:
		if home {
			return Seller, true
		}
		return "", true
	case w.account[d]:
		return CustomerNotInCRM, true
	}
	return OtherParty, true
}

// contradicts is whether an email's domain contradicts a person's side: a customer on the seller's
// domain, or a seller on the account's.
func (w *world) contradicts(i int, d string) bool {
	if w.shared[d] {
		return false
	}
	role, _ := w.role(i)
	return role == Customer && w.company[d] || role == Seller && w.account[d]
}

// byEmail adds every person holding the address.
func (w *world) byEmail(f *finding, address string) {
	for i, p := range w.people {
		for _, id := range p.Identifiers {
			if id.Kind != Email {
				continue
			}
			if a, _, ok := email(id.Value); ok && a == address {
				f.add(i, id)
			}
		}
	}
}

type mail struct {
	address, domain string
	home            bool
	// member is an address of a member of the seller's own Slack workspace, whom no domain puts on the
	// customer side.
	member bool
	ref    Identifier
}

// emailsOf is the reference's emails: the Slack profile email and the meeting tool's participant email.
func (r Ref) emailsOf(homeTeam string) []mail {
	var out []mail
	if r.Slack != nil {
		if a, d, ok := email(r.Slack.Email); ok {
			home := homeTeam != "" && r.Slack.Team == homeTeam
			out = append(out, mail{a, d, home, home,
				Identifier{Kind: Email, Value: a, Scope: r.Slack.Team, From: r.From}})
		}
	}
	if a, d, ok := email(r.Email); ok {
		out = append(out, mail{a, d, true, false, Identifier{Kind: Email, Value: a, From: r.From}})
	}
	return out
}

// platform is rung 1: the Slack author's user ID and profile email, and the meeting tool's participant
// email. An address names the person who holds it, on any domain. When none names a person, the
// domain shows a side.
func (w *world) platform(r Ref) finding {
	f := finding{basis: ByEmail}
	if r.Slack != nil {
		f.basis = BySlackProfile
		if r.Slack.User != "" && r.Slack.Team != "" {
			for i, p := range w.people {
				for _, id := range p.Identifiers {
					if id.Kind == SlackUser && id.Value == r.Slack.User && id.Scope == r.Slack.Team {
						f.add(i, id)
					}
				}
			}
		}
	}
	mails := r.emailsOf(w.k.HomeTeam)
	for _, m := range mails {
		w.byEmail(&f, m.address)
	}
	if len(f.people) > 0 {
		for _, i := range f.people {
			for _, m := range mails {
				if w.contradicts(i, m.domain) {
					if f.conflict == nil {
						f.conflict = map[int]bool{}
					}
					f.conflict[i] = true
				}
			}
		}
		return f
	}
	w.sideByDomain(&f, mails)
	return f
}

// sideByDomain sets the side the emails' domains show, or a reason when they show two.
func (w *world) sideByDomain(f *finding, mails []mail) {
	for _, m := range mails {
		side, ok := w.domainSide(m.domain, m.home)
		if !ok {
			f.shared = true
			continue
		}
		if side == "" || side == CustomerNotInCRM && m.member {
			continue
		}
		if f.side == "" {
			f.side, f.ref, f.domain, f.basis = side, m.ref, m.domain, ByDomain
		} else if f.side != side || side == OtherParty && f.domain != m.domain {
			f.reason = CandidatesOnBothSides
		}
	}
}

// attendees is rung 2: the attendee rows whose label is the speaker's. One row names the person who holds
// its email, or shows a side by its domain. Several rows are people behind one microphone, a room or a
// shared device: it shows a side only when every one of them is on it.
func (w *world) attendees(r Ref, label string, anon bool) finding {
	f := finding{basis: ByAttendeeList}
	if r.Source != Call || label == "" || anon {
		return f
	}
	rows := map[string]bool{}
	var order []string
	for _, a := range r.Attendees {
		if Label(a.Label) != label {
			continue
		}
		address, _, ok := email(a.Email)
		if !ok {
			address = ""
		}
		if !rows[address] {
			rows[address] = true
			order = append(order, address)
		}
	}
	sort.Strings(order)
	switch len(order) {
	case 0:
		return f
	case 1:
		address := order[0]
		if address == "" {
			return f
		}
		w.byEmail(&f, address)
		_, d, _ := email(address)
		m := mail{address, d, true, false, Identifier{Kind: Email, Value: address, From: r.From}}
		if len(f.people) > 0 {
			for _, i := range f.people {
				if w.contradicts(i, d) {
					if f.conflict == nil {
						f.conflict = map[int]bool{}
					}
					f.conflict[i] = true
				}
			}
			return f
		}
		w.sideByDomain(&f, []mail{m})
		return f
	}
	f.basis = ByRoom
	var side Role
	var sideDomain string
	for n, address := range order {
		var this Role
		var thisDomain string
		if address != "" {
			var one finding
			w.byEmail(&one, address)
			_, d, _ := email(address)
			switch len(one.people) {
			case 0:
				if s, ok := w.domainSide(d, true); ok && s != "" {
					this, thisDomain = class(s), d
				}
			case 1:
				role, _ := w.role(one.people[0])
				if role != Unknown {
					this = class(role)
				}
			}
		}
		if this == "" || n > 0 && (this != side || this == OtherParty && thisDomain != sideDomain) {
			f.reason = RoomMixed
			return f
		}
		side, sideDomain = this, thisDomain
	}
	f.side = side
	if side == OtherParty {
		f.domain = sideDomain
	}
	return f
}

// alias is rung 3: the aliases for the label. One for this call applies; one for every call applies
// unless the label is anonymous.
func (w *world) alias(r Ref, label string, anon bool) finding {
	f := finding{basis: ByAlias}
	if label == "" {
		return f
	}
	for _, a := range w.k.Aliases {
		if Label(a.Label) != label {
			continue
		}
		if a.Call != "" && a.Call != r.CallID || a.Call == "" && anon {
			continue
		}
		for i, p := range w.people {
			if p.Key == a.Person {
				f.add(i, Identifier{Kind: Alias, Value: label, Scope: a.Call, From: a.From})
			}
		}
	}
	return f
}

// exact is rung 4: the label equals a person's name or seller entry, compared as Label puts them. A Slack
// author's name never makes a customer, so for Slack only sellers are compared.
func (w *world) exact(r Ref, label string, anon bool) finding {
	f := finding{basis: ByName}
	if label == "" || anon {
		return f
	}
	for i, p := range w.people {
		if r.Source == Slack && !w.sellerFact[i] {
			continue
		}
		for _, id := range p.Identifiers {
			if (id.Kind == Name || id.Kind == ListedSeller) && Label(id.Value) == label {
				f.add(i, id)
			}
		}
	}
	return f
}

// variant is rung 5, when it is on: the label's Variant equals the Variant of a name of exactly one of
// the meeting's known participants, the people the attendee list's emails name.
func (w *world) variant(r Ref, label string, anon bool) finding {
	f := finding{basis: ByNameVariant}
	if !w.k.NameVariants || r.Source != Call || label == "" || anon {
		return f
	}
	v := Variant(label)
	if v == "" {
		return f
	}
	var participants finding
	for _, a := range r.Attendees {
		if address, _, ok := email(a.Email); ok {
			w.byEmail(&participants, address)
		}
	}
	for _, i := range participants.people {
		for _, id := range w.people[i].Identifiers {
			if (id.Kind == Name || id.Kind == ParticipantName) && Variant(id.Value) == v {
				f.add(i, id)
			}
		}
	}
	return f
}
