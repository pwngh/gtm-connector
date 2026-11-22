package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/sources/salesforce/salesforcetest"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// Each install command prints what it takes with --help or -h, on stdout, and exits 0 with nothing done.
func TestInstallCommandsPrintTheirHelp(t *testing.T) {
	for _, name := range []string{"setup", "serve", "backup", "restore", "health", "version"} {
		for _, flag := range []string{"--help", "-h"} {
			var out, errs bytes.Buffer
			code := dispatch(ctx, []string{name, flag}, screen{out: &out, err: &errs})
			if code != 0 || !strings.HasPrefix(out.String(), "usage: gtm-connector "+name) || errs.Len() != 0 {
				t.Errorf("%s %s: exit %d\n%s%s", name, flag, code, out.String(), errs.String())
			}
		}
	}
}

// accounts finds a Salesforce account by its exact AccountNumber, and its table shows the number, since a
// Salesforce account carries no renewal date of its own. The demo book has no account numbers.
func TestAccountsFindsASalesforceAccountByItsNumber(t *testing.T) {
	d := setUpDemo(t)
	if err := d.demo(t, d.appURL); err != nil {
		t.Fatal(err)
	}
	org, _, _ := inventedSalesforce(t)
	numbered, err := strictjson.LoadsText(`{"Account": [{"Id": "001000000000998AAA", "Name": "Birch  Mills plc",
		"Industry": "Testing", "AccountNumber": "INV-0017"}], "Contact": [], "Opportunity": [], "Contract": []}`)
	if err != nil {
		t.Fatal(err)
	}
	org.Add(numbered.(*strictjson.Object))
	where := []string{"--state-dir", "invented-books"}
	if code, _, stderr := d.read(t, append([]string{"connect", "salesforce", "--org", salesforcetest.Alias,
		"--environment", "production"}, where...)...); code != 0 {
		t.Fatalf("connect: exit %d\n%s", code, stderr)
	}
	var page struct {
		Accounts []struct {
			ID     string
			Name   string
			Number string `json:"account_number"`
		}
		More bool `json:"has_more"`
	}
	d.result(t, &page, append([]string{"accounts", "--account-number", "INV-0017"}, where...)...)
	if len(page.Accounts) != 1 || page.Accounts[0].Name != "Birch  Mills plc" || page.Accounts[0].Number != "INV-0017" {
		t.Fatalf("by number: %+v", page)
	}
	d.result(t, &page, append([]string{"accounts", "--account-number", "INV-0018"}, where...)...)
	if len(page.Accounts) != 0 {
		t.Fatalf("by a number no account has: %+v", page)
	}
	// The search is one search_accounts call whose receipt names the number.
	if n := d.count(t, `SELECT count(*) FROM receipts WHERE tool = 'search_accounts' AND status = 'ok'
		AND arguments::jsonb = '{"query": "", "account_number": "INV-0017"}'`); n != 1 {
		t.Errorf("%d receipts of the search by number", n)
	}
	for _, argv := range [][]string{
		append([]string{"accounts", "--account-number", "INV-0017", "--query", "Birch"}, where...),
		append([]string{"accounts", "--account-number", " "}, where...),
		{"accounts", "--account-number", "INV-0017"},
	} {
		if code, stdout, _ := d.read(t, append(argv, "--json")...); code != 2 || stdout != "" {
			t.Errorf("%v: exit %d\n%s", argv, code, stdout)
		}
	}
	// On a terminal the table of a Salesforce workspace has a NUMBER column, and the demo book's keeps RENEWAL.
	for _, c := range []struct {
		argv           []string
		heading, holds string
	}{
		{append([]string{"--query", "Birch"}, where...), "ID  ACCOUNT  INDUSTRY  NUMBER", "INV-0017"},
		{[]string{"--query", "Thornfield"}, "ID  ACCOUNT  INDUSTRY  RENEWAL", "2027-02-08"},
	} {
		t.Setenv("GTM_DATABASE_URL", d.appURL)
		var out, errs bytes.Buffer
		code := runCommand(ctx, "accounts", c.argv, screen{out: &out, err: &errs, terminal: true})
		heading, _, _ := strings.Cut(out.String(), "\n")
		if code != 0 || strings.Join(strings.Fields(heading), "  ") != c.heading || !strings.Contains(out.String(), c.holds) {
			t.Errorf("accounts %v on a terminal: exit %d\n%s%s", c.argv, code, out.String(), errs.String())
		}
	}
}

// brief takes an account's name where it took only its ID: one account that the name's words find is briefed,
// and stderr names it. A name that finds none or several briefs nothing and says which accounts it found.
func TestBriefTakesAnAccountsName(t *testing.T) {
	d := setUpDemo(t)
	if err := d.demo(t, d.appURL); err != nil {
		t.Fatal(err)
	}
	var card struct {
		AccountID string `json:"account_id"`
	}
	d.result(t, &card, "brief", "thornfield wren")
	if card.AccountID != "acc-003" {
		t.Fatalf("brief by name: %+v", card)
	}
	if code, stdout, stderr := d.read(t, "brief", "Thornfield  LLP"); code != 0 ||
		!strings.Contains(stderr, `Account: acc-003 "Thornfield Wren LLP".`) || !strings.Contains(stdout, "DEAL CARD") {
		t.Fatalf("brief by name as text: exit %d\n%s", code, stderr)
	}
	before := d.count(t, `SELECT count(*) FROM cards`)
	// A name no account holds is refused as the ID it may have been meant as, and one many hold names
	// the search.
	if _, message, _ := d.refusal(t, 2, "brief", "no such account anywhere"); message != "account ID must have the form acc-001" {
		t.Errorf("brief of a name no account holds: %s", message)
	}
	if _, message, next := d.refusal(t, 2, "brief", "a"); !strings.HasPrefix(message, "More than 5 accounts match.") &&
		!strings.Contains(message, "accounts match.") || !strings.Contains(next, "accounts --query") {
		t.Errorf("brief of a name many hold: %s (%s)", message, next)
	}
	// Several matches are named by ID, and the text typed is not repeated.
	if _, message, _ := d.refusal(t, 2, "brief", "Alder"); !strings.HasPrefix(message, "2 accounts match.") ||
		!strings.Contains(message, `acc-001 "Alder Vale Packaging"`) || !strings.Contains(message, "acc-012") {
		t.Errorf("brief of a name several accounts hold: %s", message)
	}
	if after := d.count(t, `SELECT count(*) FROM cards`); after != before {
		t.Errorf("a refused name published %d cards", after-before)
	}
}

// A list given once with an empty value is cleared: the seller list, the company domains and the aliases.
func TestAnEmptyValueClearsAList(t *testing.T) {
	d := setUpDemo(t)
	where := []string{"--state-dir", "lists"}
	d.connectDemo(t, "lists")
	if code, _, stderr := d.read(t, append([]string{"connect", "calls", "--seller", "Morgan Reed", "--seller", "Dana Crest",
		"--company-domain", "northwind.example", "--alias", "M. Reed=Morgan Reed"}, where...)...); code != 0 {
		t.Fatalf("connect calls: %s", stderr)
	}
	var status struct {
		Sellers []string
		Domains []string `json:"company_domains"`
		Aliases []any
	}
	d.result(t, &status, append([]string{"connect", "status"}, where...)...)
	if len(status.Sellers) != 2 || len(status.Domains) != 1 || len(status.Aliases) != 1 {
		t.Fatalf("the lists as set: %+v", status)
	}
	// One list cleared leaves the others as they were.
	if code, _, stderr := d.read(t, append([]string{"connect", "calls", "--seller", ""}, where...)...); code != 0 {
		t.Fatalf("clear the sellers: %s", stderr)
	}
	status.Sellers, status.Domains, status.Aliases = nil, nil, nil
	d.result(t, &status, append([]string{"connect", "status"}, where...)...)
	if len(status.Sellers) != 0 || len(status.Domains) != 1 || len(status.Aliases) != 1 {
		t.Fatalf("after clearing the sellers: %+v", status)
	}
	if code, _, stderr := d.read(t, append([]string{"connect", "calls", "--company-domain", "", "--alias", ""}, where...)...); code != 0 {
		t.Fatalf("clear the domains and aliases: %s", stderr)
	}
	status.Sellers, status.Domains, status.Aliases = nil, nil, nil
	d.result(t, &status, append([]string{"connect", "status"}, where...)...)
	if len(status.Sellers) != 0 || len(status.Domains) != 0 || len(status.Aliases) != 0 {
		t.Fatalf("after clearing every list: %+v", status)
	}
	// An empty value beside others is still refused.
	if code, _, _ := d.read(t, append([]string{"connect", "calls", "--seller", "", "--seller", "Morgan Reed"}, where...)...); code != 2 {
		t.Errorf("an empty seller beside another: exit %d", code)
	}
}

// What a failed Salesforce step says to do holds for a workspace that is connected as for an alias checked
// before it is: it never names a flag only one of them takes.
func TestSalesforceFixesNameNoFlagOfOneCommand(t *testing.T) {
	for code, text := range salesforceFailures {
		if strings.Contains(text[1], "--sf-org") {
			t.Errorf("%s: %s", code, text[1])
		}
	}
}
