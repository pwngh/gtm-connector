package mcp

import (
	"net/http/httptest"
	"slices"
	"testing"
	"time"
)

// What the tests of the mounted endpoint (package mcp_test) share with the gate's own: the recorded vectors,
// the fixture's bearers and the write vectors' answers.

// Target is an endpoint the recorded vectors are replayed against: how a request reaches it, and every
// outer receipt it has written so far, each as "tool status http".
type Target struct {
	Post     func(body []byte, headers [][2]string) *httptest.ResponseRecorder
	Receipts func() []string
}

// ReplayRecordedVectors sends every recorded request vector to an endpoint and checks its status, its reply
// and its receipt. It returns how many it sent.
func ReplayRecordedVectors(t *testing.T, to Target) int {
	t.Helper()
	vectors := slices.Concat(acceptVectors(), methodVectors(), schemaVectors(), shapeVectors(), ladderVectors())
	replayVectors(t, target{to.Post, to.Receipts}, vectors)
	return len(vectors)
}

// The fixture's resource and protocol revision, and its bearers by name.
const (
	FixtureResource = testResource
	FixtureVersion  = testVersion
)

func FixtureBearer(name string) string { return tokenValue(name) }

// FixtureScopes are the scopes each fixture bearer carries, by name.
func FixtureScopes() map[string][]string {
	scopes := map[string][]string{}
	for raw, token := range fixtureTokens(testWall) {
		for _, name := range []string{"crm", "calls", "slack", "write", "full", "read", "expired", "wrong_audience"} {
			if raw == tokenValue(name) {
				scopes[name] = token.Scopes
			}
		}
	}
	return scopes
}

// The answers a write vector can have, and the answer the gate gives each named write vector.
const (
	WantRefused      = wantRefused
	WantUnauthorized = wantUnauthorized
	WantWithheld     = wantWithheld
	WantUnlisted     = wantUnlisted
	InspectorListing = inspectorListing
	WriteTool        = writeTool
)

func WriteVectorAnswers() map[string]string {
	answers := map[string]string{}
	for name, vector := range writeVectors {
		answers[name] = vector.want
	}
	return answers
}

// SetSettleLimit shortens how long each receipt start and finish of a gateway may take, before it serves.
func SetSettleLimit(g *Gateway, limit time.Duration) { g.limit = limit }

// SetStdioSettleLimit is SetSettleLimit for a stdio server.
func SetStdioSettleLimit(c *StdioConfig, limit time.Duration) { c.limit = limit }

// RecordedTools are the tools the Python gateway listed, write tool included.
func RecordedTools() []string { return slices.Clone(recordedTools) }
