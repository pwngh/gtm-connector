package people_test

import (
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	p "github.com/pwngh/gtm-connector/internal/people"
)

// gen draws invented worlds and references from a small vocabulary, so names, addresses and IDs collide
// often.
type gen struct{ r *rand.Rand }

var (
	genNames    = []string{"Dana Crest", "dana crest", "Crest, Dana", "Lee Park", "Sam Reed", "Jo Ames", "Speaker 1", "Dana Crest (Acme)", "Dana Crest's iPhone", ""}
	genLocals   = []string{"dana", "lee", "sam", "jo", "ops"}
	genDomains  = []string{"acme.example", "seller.example", "counsel.example", "freemail.example", "group.example"}
	genAccounts = []string{"acc-a", "acc-p", "acc-x"}
	genTeams    = []string{"T1", "T2"}
	genUsers    = []string{"U1", "U2", "U3"}
	genCalls    = []string{"", "c1", "c2"}
)

func (g gen) pick(items []string) string { return items[g.r.IntN(len(items))] }

func (g gen) address() string {
	a := g.pick(genLocals) + "@" + g.pick(genDomains)
	if g.r.IntN(4) == 0 {
		a = strings.ToUpper(a[:1]) + a[1:]
	}
	return a
}

func (g gen) from() string { return g.pick([]string{"r1", "r2"}) }

func (g gen) identifier() p.Identifier {
	switch g.r.IntN(6) {
	case 0:
		return p.Identifier{Kind: p.Email, Value: g.address(), From: g.from()}
	case 1:
		return p.Identifier{Kind: p.SlackUser, Value: g.pick(genUsers), Scope: g.pick(genTeams), From: g.from()}
	case 2:
		return p.Identifier{Kind: p.SalesforceContact, Value: "003", Scope: g.pick(genAccounts), From: g.from()}
	case 3:
		return p.Identifier{Kind: p.SalesforceUser, Value: "005", From: g.from()}
	case 4:
		return p.Identifier{Kind: p.ListedSeller, Value: g.pick(genNames), From: g.from()}
	}
	return p.Identifier{Kind: p.Name, Value: g.pick(genNames), From: g.from()}
}

func (g gen) subset(items []string) []string {
	var out []string
	for _, item := range items {
		if g.r.IntN(3) == 0 {
			out = append(out, item)
		}
	}
	return out
}

func (g gen) alias(k p.Known) p.AliasEntry {
	person := "nobody"
	if len(k.People) > 0 {
		person = k.People[g.r.IntN(len(k.People))].Key
	}
	return p.AliasEntry{Label: g.pick(genNames), Call: g.pick(genCalls), Person: person, From: g.from()}
}

func (g gen) attendee() p.Attendee {
	return p.Attendee{Label: g.pick(genNames), Email: g.address()}
}

func (g gen) known() p.Known {
	k := p.Known{Account: "acc-a", Related: g.subset([]string{"acc-p"}), HomeTeam: "T1",
		CompanyDomains: []string{"seller.example"}, AccountDomains: g.subset(genDomains),
		SharedDomains: g.subset(genDomains), NameVariants: g.r.IntN(2) == 0}
	k.AccountDomains = append(k.AccountDomains, "acme.example")
	k.SharedDomains = append(k.SharedDomains, "freemail.example")
	for i := range g.r.IntN(6) {
		person := p.Person{Key: string(rune('a' + i))}
		for range 1 + g.r.IntN(4) {
			person.Identifiers = append(person.Identifiers, g.identifier())
		}
		k.People = append(k.People, person)
	}
	for range g.r.IntN(3) {
		k.Aliases = append(k.Aliases, g.alias(k))
	}
	return k
}

func (g gen) ref() p.Ref {
	r := p.Ref{Source: p.Call, CallID: g.pick(genCalls[1:]), Label: g.pick(genNames), From: "src"}
	if g.r.IntN(2) == 0 {
		r.Source = p.Slack
		if g.r.IntN(3) > 0 {
			r.Slack = &p.SlackAuthor{Team: g.pick(genTeams), User: g.pick(genUsers)}
			if g.r.IntN(2) == 0 {
				r.Slack.Email = g.address()
			}
		}
		return r
	}
	if g.r.IntN(3) == 0 {
		r.Email = g.address()
	}
	for range g.r.IntN(4) {
		r.Attendees = append(r.Attendees, g.attendee())
	}
	return r
}

// view is a result with the person by key, so results over permuted inputs compare.
type view struct {
	person     string
	role       p.Role
	rung       p.Rung
	basis      p.Basis
	reason     p.Reason
	matched    p.Identifier
	domain     string
	candidates string
}

func see(r p.Result) view {
	v := view{role: r.Role, rung: r.Rung, basis: r.Basis, reason: r.Reason, matched: r.Matched, domain: r.Domain,
		candidates: strings.Join(r.Candidates, ",")}
	if r.Person != nil {
		v.person = r.Person.Key
	}
	return v
}

// side is the side a result names, with a customer and a customer-side speaker as one side, or "".
func side(r p.Result) p.Role {
	switch r.Role {
	case p.CustomerNotInCRM:
		return p.Customer
	case p.Unknown:
		return ""
	}
	return r.Role
}

// sideOnly is whether a result rests on a side with no person: a domain or a room.
func sideOnly(r p.Result) bool { return r.Basis == p.ByDomain || r.Basis == p.ByRoom }

const draws = 20000

// TestStrongerIsNeverContradicted: the side a result names is never one that an identifier on a rung as
// strong or stronger, read alone, contradicts. Each part of a reference is resolved alone, and where it
// decides on its own rung, its side must agree with the whole result's.
func TestStrongerIsNeverContradicted(t *testing.T) {
	g := gen{rand.New(rand.NewPCG(1, 2))}
	for range draws {
		k, ref := g.known(), g.ref()
		whole := p.Resolve(k, ref)
		if side(whole) == "" {
			continue
		}
		noAliases := k
		noAliases.Aliases = nil
		parts := []struct {
			rung p.Rung
			k    p.Known
			ref  p.Ref
		}{
			{p.RungPlatform, k, p.Ref{Source: ref.Source, CallID: ref.CallID, Slack: ref.Slack, Email: ref.Email, From: ref.From}},
			{p.RungAttendees, noAliases, p.Ref{Source: ref.Source, CallID: ref.CallID, Label: ref.Label, Attendees: ref.Attendees, From: ref.From}},
			{p.RungAlias, k, p.Ref{Source: ref.Source, CallID: ref.CallID, Label: ref.Label, From: ref.From}},
			{p.RungName, noAliases, p.Ref{Source: ref.Source, CallID: ref.CallID, Label: ref.Label, From: ref.From}},
		}
		for _, part := range parts {
			if part.rung > whole.Rung {
				break
			}
			alone := p.Resolve(part.k, part.ref)
			if alone.Rung == part.rung && side(alone) != "" && side(alone) != side(whole) {
				t.Fatalf("rung %d alone says %s, the whole reference %s\nknown %+v\nref %+v", part.rung,
					alone.Role, whole.Role, k, ref)
			}
		}
	}
}

// weaken adds identifiers below rung n to a reference and its world: a label, attendees, aliases, names
// and rung 5.
func (g gen) weaken(k p.Known, ref p.Ref, n p.Rung) (p.Known, p.Ref) {
	k.People = slices.Clone(k.People)
	if n < p.RungAttendees {
		ref.Label = g.pick(genNames)
		ref.Attendees = slices.Clone(ref.Attendees)
		for range 1 + g.r.IntN(3) {
			ref.Attendees = append(ref.Attendees, g.attendee())
		}
	}
	if n < p.RungAlias {
		k.Aliases = slices.Clone(k.Aliases)
		for range 1 + g.r.IntN(3) {
			k.Aliases = append(k.Aliases, g.alias(k))
		}
	}
	if n < p.RungName {
		for i := range k.People {
			person := k.People[i]
			person.Identifiers = append(slices.Clone(person.Identifiers),
				p.Identifier{Kind: p.Name, Value: g.pick(genNames), From: g.from()})
			k.People[i] = person
		}
	}
	k.NameVariants = true
	return k, ref
}

// TestWeakerNeverChangesStronger: once a rung has decided, by a person or by finding several, adding
// identifiers that only weaker rungs read never changes the result. A side with no person stays: a weaker
// identifier can name a person on it or make the result unknown, never name another side.
func TestWeakerNeverChangesStronger(t *testing.T) {
	g := gen{rand.New(rand.NewPCG(3, 4))}
	decided, sides := 0, 0
	for range draws {
		k, ref := g.known(), g.ref()
		before := p.Resolve(k, ref)
		if before.Rung == p.NoRung || before.Rung == p.RungVariant {
			continue
		}
		k2, ref2 := g.weaken(k, ref, before.Rung)
		after := p.Resolve(k2, ref2)
		switch {
		case sideOnly(before):
			sides++
			if side(after) != "" && side(after) != side(before) {
				t.Fatalf("a weaker identifier turned %s into %s\nknown %+v\nref %+v", before.Role, after.Role, k2, ref2)
			}
		case before.Reason != p.RungsDisagree:
			decided++
			if see(after) != see(before) {
				t.Fatalf("a weaker identifier changed %+v into %+v\nknown %+v\nref %+v", see(before), see(after), k2, ref2)
			}
		}
	}
	if decided < draws/20 || sides < draws/50 {
		t.Fatalf("too few draws decided: %d by a person or candidates, %d by a side", decided, sides)
	}
}

// TestOrderNeverMatters: permuting every list in the input never changes the result.
func TestOrderNeverMatters(t *testing.T) {
	g := gen{rand.New(rand.NewPCG(5, 6))}
	for range draws {
		k, ref := g.known(), g.ref()
		before := see(p.Resolve(k, ref))
		shuffle := func(n int, swap func(i, j int)) { g.r.Shuffle(n, swap) }
		k.People = slices.Clone(k.People)
		shuffle(len(k.People), func(i, j int) { k.People[i], k.People[j] = k.People[j], k.People[i] })
		for i := range k.People {
			ids := slices.Clone(k.People[i].Identifiers)
			shuffle(len(ids), func(a, b int) { ids[a], ids[b] = ids[b], ids[a] })
			k.People[i].Identifiers = ids
		}
		for _, list := range []*[]string{&k.Related, &k.AccountDomains, &k.SharedDomains, &k.CompanyDomains} {
			*list = slices.Clone(*list)
			shuffle(len(*list), func(i, j int) { (*list)[i], (*list)[j] = (*list)[j], (*list)[i] })
		}
		k.Aliases = slices.Clone(k.Aliases)
		shuffle(len(k.Aliases), func(i, j int) { k.Aliases[i], k.Aliases[j] = k.Aliases[j], k.Aliases[i] })
		ref.Attendees = slices.Clone(ref.Attendees)
		shuffle(len(ref.Attendees), func(i, j int) {
			ref.Attendees[i], ref.Attendees[j] = ref.Attendees[j], ref.Attendees[i]
		})
		if after := see(p.Resolve(k, ref)); after != before {
			t.Fatalf("order changed %+v into %+v\nknown %+v\nref %+v", before, after, k, ref)
		}
	}
}
