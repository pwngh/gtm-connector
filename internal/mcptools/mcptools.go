// Package mcptools is what the MCP gate is mounted with: the token verifier, the receipt recorder and the
// read tools of one workspace, on the store.
//
// The gate (internal/transport/mcp) admits a request and owns the call's receipt: it commits one before
// anything runs and finishes it before anything is released. Everything here works under that receipt and
// never starts another for the call:
//
//   - A token opens a source by its scope and an account by its principal's live grant, and a tool needs
//     both: an account the principal is not granted is refused before any read, and a search or a list holds
//     granted accounts only.
//   - A direct read (search_accounts, get_account, list_opportunities, read_call_notes, read_slack_messages)
//     is a source read of its connection. Its body hands the recorder the record versions it returns, and
//     the recorder finishes the receipt by binding exactly those.
//   - prepare_renewal_brief runs the card maker under the gate's receipt, which is the brief's seal: the
//     publication finishes it with the saved bytes' digest. Each read inside it commits a child receipt of
//     its own, checks the bearer again and counts one attempt against its rate window.
//   - list_deals and read_deal_card read no source and open none. They give the deals of the accounts the
//     principal is granted and the selection holds, and a deal's newest card whole, under the grant checks
//     every surface applies (internal/authz), and a card only on the terms read_renewal_brief releases a
//     saved brief on.
//   - read_renewal_brief reads no source. It releases a saved brief only while its seal, the selection, the
//     source bindings, the principal's grant and the scopes the bearer carries at release still cover it.
package mcptools

import (
	"context"
	"crypto/sha256"
	"errors"
	"time"

	"github.com/pwngh/gtm-connector/internal/authz"
	"github.com/pwngh/gtm-connector/internal/cards"
	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/strictjson"
	"github.com/pwngh/gtm-connector/internal/transport/mcp"
)

// Storage is what the verifier and the tools read from the store: a token, a saved brief, and the evidence
// reads a principal makes under its grants (its accounts, its deals and their cards).
type Storage interface {
	authz.Store
	MachineToken(ctx context.Context, workspaceID string, tokenSHA256 []byte) (postgres.MachineToken, error)
	CardByBrief(ctx context.Context, workspaceID, briefID string) (postgres.Card, error)
	// Deal is a deal of the workspace with no grant checked, which a receipt names the account of.
	Deal(ctx context.Context, workspaceID, ref string) (postgres.Deal, error)
}

// Book is a source that also answers the two CRM reads no card makes: a search, and the opportunities of
// every account. accounts limits either to those IDs. A search with a number looks an exact AccountNumber
// up, which only a CRM that has account numbers answers; another refuses it with a tool error. Each record
// carries its record_ref.
type Book interface {
	cards.Source
	Search(ctx context.Context, query string, number *string, accounts []string) (records []*strictjson.Object,
		more bool, err error)
	Opportunities(ctx context.Context, accounts []string) ([]*strictjson.Object, error)
}

// Config is one workspace's tools and how its callers are checked.
type Config struct {
	// Resource is the exact https://host:port/mcp URL the endpoint is served at. Issuer is who issues its
	// tokens, as the protected-resource metadata names them.
	Resource, Issuer string
	WorkspaceID      string
	Store            Storage
	Receipts         receipts.Store
	Source           Book
	Sources          evidence.Sources
	// Connections names each source system's connection ("crm", "calls", "slack").
	Connections map[string]string
	// Open, when set, gives the workspace's sources as they are connected at the moment of a call, in place
	// of Source, Sources and Connections. Every call asks it once, before its receipt starts, and reads only
	// what it gave: a source detached or connected again is not read by a server that is still running. An
	// error it returns is the call's refusal, under the call's receipt.
	Open func(ctx context.Context) (Opened, error)
	// Bound, when set, gives the sources the workspace is bound to at the moment of a call, read from the
	// store without opening any source, in place of Sources. read_deal_card releases a card only when it was
	// read from these.
	Bound func(ctx context.Context) (evidence.Sources, error)
	// Selection limits every read to its accounts, and Slack to those it marks, and adds the two brief
	// tools. Nil serves every account the caller's principal is granted, and no brief tool.
	Selection cards.Selection
	// RateLimit is how many calls one token may make in WindowSeconds. Zero is 60 calls in 60 seconds.
	RateLimit     int
	WindowSeconds float64
	// Clock, Wall, Now and NewID replace the gate's clocks and the maker's clock and IDs in tests.
	Clock, Wall func() float64
	Now         func() time.Time
	NewID       func() string
}

// Opened is a workspace's sources as one call reads them.
type Opened struct {
	Source      Book
	Sources     evidence.Sources
	Connections map[string]string
	// Generations is the generation of each source's connection that Source was opened under. A call whose
	// receipt starts under another generation is refused and reads nothing.
	Generations map[string]int64
}

// server is the tools of one workspace.
type server struct {
	Config
	recorder *recorder
}

// newServer is a workspace's tools, verifier and recorder.
func newServer(c Config) (*server, error) {
	if c.WorkspaceID == "" || c.Store == nil || c.Receipts == nil || c.Source == nil && c.Open == nil {
		return nil, errors.New("the tools need a workspace, its store, its receipts and its source")
	}
	if c.RateLimit == 0 && c.WindowSeconds == 0 {
		c.RateLimit, c.WindowSeconds = 60, 60
	}
	if c.Bound == nil {
		fixed := c.Sources
		c.Bound = func(context.Context) (evidence.Sources, error) { return fixed, nil }
	}
	s := &server{Config: c}
	sources := c.Open
	if sources == nil {
		fixed := Opened{Source: c.Source, Sources: c.Sources, Connections: c.Connections}
		sources = func(context.Context) (Opened, error) { return fixed, nil }
	}
	s.recorder = &recorder{store: c.Receipts, workspace: c.WorkspaceID, tokens: c.Store, deals: c.Store,
		sources: sources, open: map[string]*pending{}}
	return s, nil
}

// New builds the gateway that serves a workspace's tools at its resource.
func New(c Config) (*mcp.Gateway, error) {
	s, err := newServer(c)
	if err != nil {
		return nil, err
	}
	return mcp.NewGateway(mcp.GatewayConfig{Resource: s.Resource, Issuer: s.Issuer, RateLimit: s.RateLimit,
		WindowSeconds: s.WindowSeconds, Selected: s.Selection != nil, Verify: s.verify, Receipts: s.recorder,
		Run: s.run, Clock: s.Clock, Wall: s.Wall})
}

// Stdio is the same tools, verifier and recorder for mcp.ServeStdio, served as the machine principal that
// holds token. Every request is verified against the store with that token, as an HTTPS request is with its
// bearer, so its expiry, revocation, scopes and principal's grants apply on stdio as they do over HTTPS, and
// with Open every call reads the sources as they are connected at that moment, as over HTTPS.
func Stdio(c Config, token string) (mcp.StdioConfig, error) {
	s, err := newServer(c)
	if err != nil {
		return mcp.StdioConfig{}, err
	}
	return mcp.StdioConfig{Token: token, Resource: s.Resource, RateLimit: s.RateLimit, WindowSeconds: s.WindowSeconds,
		Selected: s.Selection != nil, Verify: s.verify, Receipts: s.recorder, Run: s.run, Clock: s.Clock,
		Wall: s.Wall}, nil
}

// lookup bounds one read of the token table. The gate's verifier takes no context of its own.
const lookup = 5 * time.Second

// verify is the gate's verifier: the token a bearer names in this workspace, or nil. A token that is
// unknown, revoked, of a disabled principal, of another workspace or limited to a selection is nil, and so
// is any token while the store cannot be read. The gate checks the expiry and the scopes of what this
// returns.
func (s *server) verify(raw string) *mcp.Token {
	ctx, cancel := context.WithTimeout(context.Background(), lookup)
	defer cancel()
	digest := sha256.Sum256([]byte(raw))
	token, err := s.Store.MachineToken(ctx, s.WorkspaceID, digest[:])
	if err != nil || !token.Usable {
		return nil
	}
	expires := float64(token.ExpiresAt.UnixMicro()) / 1e6
	return &mcp.Token{Scopes: token.Scopes, ExpiresAt: &expires, Resource: s.Resource, Principal: token.PrincipalID}
}
