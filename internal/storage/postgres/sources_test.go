package postgres_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/storage/postgres/postgrestest"
)

// UpdateSources saves what decide returns, moves a connection to its next generation only when its mode,
// its binding or its identity changes, and saves nothing when decide fails.
func TestUpdateSourcesSavesAChangeAndFencesIt(t *testing.T) {
	f := postgrestest.New(t)
	live, err := f.Store.EnsureWorkspace(ctx, "live", "live", "A live workspace")
	ok(t, err)
	digest := strings.Repeat("ab", 32)
	connect := func(mode, binding, identity string, sellers []string) []postgres.Connection {
		t.Helper()
		after, err := f.Store.UpdateSources(ctx, live, func([]postgres.Connection) (postgres.SourceUpdate, error) {
			return postgres.SourceUpdate{Changes: []postgres.SourceChange{{Source: "crm", Mode: mode, Binding: binding,
				IdentitySHA256: identity}}, Sellers: sellers}, nil
		})
		ok(t, err)
		return after
	}
	first := connect("sf", `{"org": "invented-alias"}`, digest, []string{"Dana Crest", "U0731P9Q2R"})
	if len(first) != 1 || first[0].Mode != "sf" || first[0].Generation != 1 || first[0].Binding != `{"org": "invented-alias"}` ||
		first[0].IdentitySHA256 != digest {
		t.Fatalf("the saved connection: %+v", first)
	}
	// The same row again is no change, and a list that is not given is kept.
	if again := connect("sf", `{"org": "invented-alias"}`, digest, nil); !slices.Equal(again, first) {
		t.Fatalf("saving the same connection changed it: %+v", again)
	}
	sellers, err := f.Store.Sellers(ctx, live.ID)
	ok(t, err)
	if strings.Join(sellers, ", ") != "Dana Crest, U0731P9Q2R" {
		t.Fatalf("the saved sellers: %v", sellers)
	}
	for i, change := range [][3]string{{"sf", `{"org": "another-alias"}`, digest}, {"sf", `{"org": "another-alias"}`, ""},
		{"none", "", ""}} {
		after := connect(change[0], change[1], change[2], nil)
		if after[0].ID != first[0].ID || after[0].Generation != int64(i+2) {
			t.Fatalf("change %d left the connection at %+v", i+1, after[0])
		}
	}
	listed, err := f.Store.Connections(ctx, live.ID)
	ok(t, err)
	if len(listed) != 1 || listed[0].Mode != "none" || listed[0].Binding != "{}" || listed[0].IdentitySHA256 != "" {
		t.Fatalf("a detached connection keeps %+v", listed)
	}
	// An empty list replaces the saved one.
	connect("none", "", "", []string{})
	if sellers, err = f.Store.Sellers(ctx, live.ID); err != nil || len(sellers) != 0 {
		t.Fatalf("the sellers after an empty list: %v %v", sellers, err)
	}

	// decide sees the connections as they are, and its refusal saves nothing.
	refused := errors.New("refused by the caller")
	_, err = f.Store.UpdateSources(ctx, live, func(current []postgres.Connection) (postgres.SourceUpdate, error) {
		if !slices.Equal(current, listed) {
			t.Errorf("decide was given %+v", current)
		}
		return postgres.SourceUpdate{Changes: []postgres.SourceChange{{Source: "crm", Mode: "sf"}}}, refused
	})
	is(t, err, refused)
	if after, _ := f.Store.Connections(ctx, live.ID); !slices.Equal(after, listed) {
		t.Fatalf("a refused change was saved: %+v", after)
	}
	_, err = f.Store.UpdateSources(ctx, postgres.Workspace{ID: "00000000-0000-4000-8000-000000000000", Kind: "live"},
		func([]postgres.Connection) (postgres.SourceUpdate, error) { return postgres.SourceUpdate{}, nil })
	is(t, err, postgres.ErrNotFound)

	// The database holds a row to the rules whoever writes it: demo data never joins a workspace of real
	// sources, a binding is a JSON object of bounded size, and a seller list is at most 200 names of 1 to
	// 200 characters.
	many := slices.Repeat([]string{"Dana Crest"}, 201)
	for name, update := range map[string]postgres.SourceUpdate{
		"demo data in a live workspace": {Changes: []postgres.SourceChange{{Source: "calls", Mode: "demo"}}},
		"a binding that is no object":   {Changes: []postgres.SourceChange{{Source: "crm", Mode: "sf", Binding: `"alias"`}}},
		"a binding past 4096 bytes": {Changes: []postgres.SourceChange{{Source: "crm", Mode: "sf",
			Binding: `{"org": "` + strings.Repeat("a", 4096) + `"}`}}},
		"201 sellers":                {Sellers: many},
		"an empty seller":            {Sellers: []string{"Dana Crest", ""}},
		"a seller of 201":            {Sellers: []string{strings.Repeat("é", 201)}},
		"a source of another name":   {Changes: []postgres.SourceChange{{Source: "billing", Mode: "sf"}}},
		"a binding that is not JSON": {Changes: []postgres.SourceChange{{Source: "crm", Mode: "sf", Binding: `{org}`}}},
	} {
		_, err := f.Store.UpdateSources(ctx, live, func([]postgres.Connection) (postgres.SourceUpdate, error) {
			return update, nil
		})
		if err == nil {
			t.Errorf("%s was saved", name)
		}
	}
	if _, err := f.Store.UpdateSources(ctx, live, func([]postgres.Connection) (postgres.SourceUpdate, error) {
		return postgres.SourceUpdate{Sellers: append(many[:199], strings.Repeat("é", 200))}, nil
	}); err != nil {
		t.Errorf("200 sellers, one of 200 characters: %v", err)
	}
}
