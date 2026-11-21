package main

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"time"

	"golang.org/x/sys/unix"

	"github.com/pwngh/gtm-connector/internal/cards"
	"github.com/pwngh/gtm-connector/internal/sources/slack"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// Live Slack: the mapped thread of an account, read through internal/sources/slack, as sources.go's contract
// wires a source. The opener reads the row and nothing else; the reader takes the saved sign-in under its
// lock on its first read, and checks then that it is the identity the row is pinned to.
//
// The saved sign-in is a credential, so it is kept in the credentials folder setup makes, never in the
// database: the file slack-SLUG.json, mode 0600, one for each workspace, written whole by a rename, beside
// its lock slack-SLUG.lock. A backup leaves it out: a refresh token is spent once, so a restored copy is one
// Slack no longer accepts, and debug slack connect makes a new one. The mapped threads are rows in
// PostgreSQL (postgres.SaveSlackMapping), which hold no credential.

// The Slack the commands talk to. Tests set an invented one; nil is Slack itself, over the reader's own
// transport, with the system's clocks.
var (
	slackTransport http.RoundTripper
	slackClock     slack.Clock
)

// slackSignIn is the saved Slack sign-in of one workspace, by the workspace's slug.
type slackSignIn struct{ dir, slug string }

// signInOf is the saved sign-in of the workspace with this slug, in the install's credentials folder.
func signInOf(slug string) (slackSignIn, error) {
	c, err := loadConfig()
	if err != nil {
		return slackSignIn{}, local("%s", err)
	}
	return slackSignIn{dir: c.credentialsDir, slug: slug}, nil
}

func (s slackSignIn) file() string { return "slack-" + s.slug + ".json" }

// Hold takes the sign-in's lock without waiting and runs held with the sign-in. A folder that is missing is
// slack_storage_failed, and one that is not private is slack_private_state_invalid.
func (s slackSignIn) Hold(ctx context.Context, held func(slack.Saved) error) error {
	creds, err := openCredentials(s.dir, false)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return &slack.Error{Code: "slack_storage_failed"}
	case err != nil:
		return &slack.Error{Code: "slack_private_state_invalid"}
	}
	defer creds.Close()
	lock, err := unix.Openat(creds.fd, "slack-"+s.slug+".lock", unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return &slack.Error{Code: "slack_private_state_invalid"}
	}
	defer unix.Close(lock)
	var stat unix.Stat_t
	if err := unix.Fstat(lock, &stat); err != nil || private(stat, false) != nil {
		return &slack.Error{Code: "slack_private_state_invalid"}
	}
	if err := unix.Flock(lock, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) {
			return &slack.Error{Code: "slack_connection_busy"}
		}
		return &slack.Error{Code: "slack_storage_failed"}
	}
	defer unix.Flock(lock, unix.LOCK_UN)
	return held(savedSignIn{creds, s.file()})
}

// savedSignIn is the sign-in's file under its lock.
type savedSignIn struct {
	creds *credentials
	name  string
}

func (s savedSignIn) Load(context.Context) (*slack.Credentials, error) {
	raw, err := s.creds.read(s.name)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, &slack.Error{Code: "slack_private_state_invalid"}
	}
	return slack.DecodeCredentials(raw)
}

func (s savedSignIn) Save(_ context.Context, document *slack.Credentials) error {
	raw, err := slack.EncodeCredentials(document)
	if err != nil {
		return err
	}
	if err := s.creds.write(s.name, raw); err != nil {
		return &slack.Error{Code: "slack_storage_failed"}
	}
	return nil
}

func (s savedSignIn) Remove(context.Context) (bool, error) {
	if err := unix.Unlinkat(s.creds.fd, s.name, 0); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return false, nil
		}
		return false, &slack.Error{Code: "slack_storage_failed"}
	}
	if err := unix.Fsync(s.creds.fd); err != nil {
		return true, &slack.Error{Code: "slack_storage_failed"}
	}
	return true, nil
}

// signedIn is the saved sign-in as map and connect slack look at it, under its lock: refused when there is
// none, when it needs a new login, and when its identity has not been checked.
func (s slackSignIn) signedIn(ctx context.Context) (*slack.Credentials, error) {
	var found *slack.Credentials
	err := s.Hold(ctx, func(saved slack.Saved) error {
		var err error
		found, err = saved.Load(ctx)
		return err
	})
	switch {
	case err != nil:
		return nil, err
	case found == nil:
		return nil, &slack.Error{Code: "slack_not_connected"}
	case found.NeedsLogin(clockNow()) || found.State == "refresh_pending":
		return nil, &slack.Error{Code: "slack_reconnect_required"}
	case found.State != "ready":
		return nil, &slack.Error{Code: "slack_identity_unchecked"}
	}
	return found, nil
}

// clockNow is the wall clock the Slack commands read.
func clockNow() time.Time {
	if slackClock.Now != nil {
		return slackClock.Now()
	}
	return time.Now()
}

// slackMappings is a workspace's mapped threads as the reader takes them. A mapping made under another CRM
// binding than the workspace's is slack_source_mismatch.
type slackMappings struct {
	store     *postgres.Store
	workspace string
	crm       any
}

func (m slackMappings) Mapping(ctx context.Context, accountID string) (*strictjson.Object, error) {
	saved, err := m.store.SlackMappingOf(ctx, m.workspace, accountID)
	if err != nil {
		return nil, err
	}
	if saved == "" {
		return nil, &slack.Error{Code: "slack_mapping_missing"}
	}
	value, err := strictjson.Loads([]byte(saved))
	mapping, isObject := value.(*strictjson.Object)
	if err != nil || !isObject || mapping == nil {
		return nil, &slack.Error{Code: "slack_mapping_invalid"}
	}
	if !sameValue(get(mapping, "crm_binding"), m.crm) {
		return nil, &slack.Error{Code: "slack_source_mismatch"}
	}
	return mapping, nil
}

// sameValue is whether two values are the same JSON.
func sameValue(a, b any) bool {
	left, errA := strictjson.Dumps(a, strictjson.Prepared)
	right, errB := strictjson.Dumps(b, strictjson.Prepared)
	return errA == nil && errB == nil && left == right
}

// liveSlack is a workspace's live Slack: the reader of its mapped threads.
type liveSlack struct{ reader *slack.Reader }

// Messages reads the thread mapped to the account, with its authors. An account with no thread is not
// selected, and a refusal is the diagnostic its code names.
func (l liveSlack) Messages(ctx context.Context, accountID string) (messages, members []*strictjson.Object,
	snapshot *strictjson.Object, err error) {
	messages, members, snapshot, err = l.reader.ReadThreadWithMembers(ctx, accountID)
	var refused *slack.Error
	switch {
	case errors.As(err, &refused) && refused.Code == "slack_mapping_missing":
		return nil, nil, nil, cards.ErrNotSelected
	case errors.As(err, &refused):
		failed := slackSourceFailure(refused)
		return nil, nil, nil, &cards.Diagnostic{Exit: failed.code, Message: failed.message, Next: failed.next}
	}
	return messages, members, snapshot, err
}

// openSlack opens the live Slack of a workspace from its row: the binding it is pinned to, its mapped
// threads and its saved sign-in. It reads nothing.
func openSlack(_ context.Context, o opening) (slackSource, error) {
	signIn, err := signInOf(o.workspace.Slug)
	if err != nil {
		return nil, err
	}
	binding := pinnedBinding("provider", "slack", o.connection).(*strictjson.Object)
	return liveSlack{&slack.Reader{Mappings: slackMappings{o.store, o.workspace.ID, o.crmBinding},
		Connection: signIn, Binding: binding, Transport: slackTransport, Clock: slackClock}}, nil
}

// attachSlack is connect slack: it checks the workspace's saved sign-in, which must be ready, and pins the
// row to its identity. It contacts nobody.
func attachSlack(ctx context.Context, a attaching) (attached, error) {
	name, given := a.args.text("--state-dir")
	if !given {
		name = defaultWorkspace
	}
	signIn, err := signInOf(workspaceSlug(name))
	if err != nil {
		return attached{}, err
	}
	saved, err := signIn.signedIn(ctx)
	var refused *slack.Error
	switch {
	case errors.As(err, &refused) && refused.Code == "slack_not_connected":
		here := ""
		if name != defaultWorkspace {
			here = " --state-dir " + shellQuote(name)
		}
		return attached{}, &failure{code: 2, next: "gtm-connector debug slack connect --help",
			message: "No Slack sign-in is saved for this workspace. Run gtm-connector debug slack connect" + here +
				" --client-id PUBLIC_CLIENT_ID --redirect-uri REGISTERED_REDIRECT first."}
	case errors.As(err, &refused):
		return attached{}, slackSourceFailure(refused)
	case err != nil:
		return attached{}, err
	}
	binding, err := slack.IdentityBinding(saved.Identity)
	if err != nil {
		return attached{}, slackSourceFailure(err.(*slack.Error))
	}
	return attached{identity: get(binding, "identity_sha256").(string)}, nil
}
