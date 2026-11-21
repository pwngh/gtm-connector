// Package mcptools is what the MCP gate is mounted with: the token verifier, the receipt recorder and the
// read tools of one workspace, on the store.
//
// The gate (internal/transport/mcp) admits a request and owns the call's receipt: it commits one before
// anything runs and finishes it before anything is released. Everything here works under that receipt and
// never starts another for the call:
//
//   - A direct read (search_accounts, get_account, list_opportunities, read_call_notes, read_slack_messages)
//     is a source read of its connection. Its body hands the recorder the record versions it returns, and
//     the recorder finishes the receipt by binding exactly those.
//   - prepare_renewal_brief runs the card maker under the gate's receipt, which is the brief's seal: the
//     publication finishes it with the saved bytes' digest. Each read inside it commits a child receipt of
//     its own, checks the bearer again and counts one attempt against its rate window.
//   - read_renewal_brief reads no source. It releases a saved brief only while its seal, the selection, the
//     caller's scopes and the source bindings still cover it.
package mcptools

import (
	"context"
	"crypto/sha256"
	"errors"
	"sort"
	"time"

	"github.com/pwngh/gtm-connector/internal/cards"
	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/strictjson"
	"github.com/pwngh/gtm-connector/internal/transport/mcp"
)

// Storage is what the verifier and read_renewal_brief read from the store.
type Storage interface {
	MachineToken(ctx context.Context, workspaceID string, tokenSHA256 []byte) (postgres.MachineToken, error)
	CardByBrief(ctx context.Context, workspaceID, briefID string) (postgres.Card, error)
}

// Book is a source that also answers the two CRM reads no card makes: a search, and the opportunities of
// every account. accounts, when not nil, limits either to those IDs. demo.Store is one.
type Book interface {
	cards.Source
	Search(query string, accounts []string) (records []*strictjson.Object, more bool)
	Opportunities(accounts []string) []*strictjson.Object
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
	// Selection limits every read to its accounts, and Slack to those it marks, and adds the two brief
	// tools. Nil serves every account and no brief tool.
	Selection cards.Selection
	// RateLimit is how many calls one token may make in WindowSeconds. Zero is 60 calls in 60 seconds.
	RateLimit     int
	WindowSeconds float64
	// Clock, Wall, Now and NewID replace the gate's clocks and the maker's clock and IDs in tests.
	Clock, Wall func() float64
	Now         func() time.Time
	NewID       func() string
}

// server is the tools of one workspace.
type server struct {
	Config
	recorder *recorder
	maker    *cards.Maker
	selected []string // the selected account IDs in order, nil without a selection
}

// New builds the gateway that serves a workspace's tools at its resource.
func New(c Config) (*mcp.Gateway, error) {
	if c.WorkspaceID == "" || c.Store == nil || c.Receipts == nil || c.Source == nil {
		return nil, errors.New("the tools need a workspace, its store, its receipts and its source")
	}
	if c.RateLimit == 0 && c.WindowSeconds == 0 {
		c.RateLimit, c.WindowSeconds = 60, 60
	}
	s := &server{Config: c}
	s.recorder = &recorder{store: c.Receipts, workspace: c.WorkspaceID, tokens: c.Store, connections: c.Connections,
		open: map[string]*pending{}}
	s.maker = &cards.Maker{Receipts: c.Receipts, Source: c.Source, Sources: c.Sources, Connections: c.Connections,
		NewID: c.NewID, Now: c.Now}
	if c.Selection != nil {
		s.selected = []string{}
		for account := range c.Selection {
			s.selected = append(s.selected, account)
		}
		sort.Strings(s.selected)
	}
	return mcp.NewGateway(mcp.GatewayConfig{Resource: c.Resource, Issuer: c.Issuer, RateLimit: c.RateLimit,
		WindowSeconds: c.WindowSeconds, Selected: c.Selection != nil, Verify: s.verify, Receipts: s.recorder,
		Run: s.run, Clock: c.Clock, Wall: c.Wall})
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
