package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pwngh/gtm-connector/internal/receipts"
	"github.com/pwngh/gtm-connector/internal/storage/postgres"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// sessionToken is the one token a stdio process made, read by the owner.
func sessionToken(t *testing.T, d *demoDatabase) postgres.ClientToken {
	t.Helper()
	tokens, err := d.owner.MachineTokens(ctx, d.workspace.ID)
	if err != nil || len(tokens) != 1 {
		t.Fatalf("tokens %+v %v", tokens, err)
	}
	return tokens[0]
}

// waitExit waits for a process to end.
func waitExit(t *testing.T, done <-chan struct{}, within time.Duration) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(within):
		t.Fatalf("mcp stdio did not end within %s", within)
	}
}

// The process's token lasts two minutes unless renewed, so a process that is killed leaves it usable for at
// most that long.
func TestStdioTokenLastsTwoMinutes(t *testing.T) {
	d := stdioDemo(t)
	p := startStdio(t, d, "--scope", "crm:read")
	p.send(stdioStart, stdioReady)
	p.answer(1)
	token := sessionToken(t, d)
	if lifetime := token.ExpiresAt.Sub(token.CreatedAt); lifetime > 2*time.Minute || lifetime < time.Minute {
		t.Fatalf("the session token lasts %s", lifetime)
	}
	p.stop()
}

// A client that closes its end of stdout ends the session through the normal path, SIGPIPE ignored: the
// process exits 0 and revokes its token, and no receipt is left pending.
func TestStdioClosedStdoutRevokesTheToken(t *testing.T) {
	d := stdioDemo(t)
	command := exec.Command(buildBinary(t), "mcp", "stdio", "--scope", "crm:read")
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "GTM_DATABASE_URL=" + d.appURL}
	in, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	line := make([]byte, 1)
	io.WriteString(in, stdioStart+"\n")
	if _, err := out.Read(line); err != nil {
		t.Fatal(err)
	}
	out.(*os.File).Close()
	done := make(chan struct{})
	var exit error
	go func() { exit = command.Wait(); close(done) }()
	io.WriteString(in, stdioReady+"\n"+stdioCall(2, "get_account", `{"account_id":"acc-003"}`)+"\n")
	waitExit(t, done, 30*time.Second)
	if exit != nil {
		t.Fatalf("mcp stdio after its stdout closed: %v; stderr %s", exit, stderr.String())
	}
	if token := sessionToken(t, d); token.RevokedAt == nil {
		t.Fatal("the session token was left live")
	}
	if got := d.count(t, `SELECT count(*) FROM receipts WHERE status = 'pending'`); got != 0 {
		t.Fatalf("%d receipts left pending", got)
	}
}

// A client that stops reading while its stdin stays open does not hold the session: once a write has waited
// the write timeout the process ends and revokes its token.
func TestStdioClientThatStopsReadingRevokesTheToken(t *testing.T) {
	d := stdioDemo(t)
	command := exec.Command(buildBinary(t), "mcp", "stdio", "--scope", "crm:read")
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "GTM_DATABASE_URL=" + d.appURL}
	in, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := command.StdoutPipe() // never read
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	done := make(chan struct{})
	go func() { command.Wait(); close(done) }()
	go func() {
		io.WriteString(in, stdioStart+"\n"+stdioReady+"\n")
		for id := 2; id < 300; id++ {
			if _, err := io.WriteString(in, `{"jsonrpc":"2.0","id":`+strconv.Itoa(id)+`,"method":"tools/list"}`+"\n"); err != nil {
				return
			}
		}
	}()
	waitExit(t, done, 60*time.Second)
	if token := sessionToken(t, d); token.RevokedAt == nil {
		t.Fatal("the session token was left live")
	}
}

// A revocation that fails is said on stderr in one fixed line.
func TestStdioRevocationFailureIsSaid(t *testing.T) {
	d := stdioDemo(t)
	store, err := postgres.Open(ctx, d.appURL)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	var stderr bytes.Buffer
	revokeSession(ctx, store, d.workspace.ID, "6f1c2f0e-0000-4000-8000-000000000000", &stderr)
	if stderr.String() != notRevoked+"\n" {
		t.Fatalf("stderr %q", stderr.String())
	}
}

// A pending receipt whose worker's lease expired is finished when mcp stdio starts, and when setup runs, so
// an install that never runs serve keeps none.
func TestStdioAndSetupFinishInterruptedReceipts(t *testing.T) {
	d := stdioDemo(t)
	interrupted := func() {
		t.Helper()
		store := &postgres.ReceiptStore{Store: d.owner, Lease: time.Millisecond}
		if _, err := store.Start(ctx, receipts.Start{WorkspaceID: d.workspace.ID, OperationID: "mcp-" + receipts.RandomID(),
			TokenID: receipts.Anonymous, Tool: "get_account", Arguments: strictjson.NewObject(), RequestID: receipts.Null()}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	interrupted()
	p := startStdio(t, d, "--scope", "crm:read")
	p.send(stdioStart, stdioReady)
	p.answer(1)
	if got := d.count(t, `SELECT count(*) FROM receipts WHERE status = 'pending'`); got != 0 {
		t.Fatalf("mcp stdio left %d interrupted receipts pending", got)
	}
	p.stop()

	interrupted()
	t.Setenv("GTM_OWNER_DATABASE_URL", d.ownerURL)
	t.Setenv("GTM_DATABASE_URL", d.appURL)
	t.Setenv("GTM_CREDENTIALS_DIR", privateDir(t))
	c, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if err := setUp(ctx, c); err != nil && !strings.Contains(err.Error(), "demo credential") {
		t.Fatal(err)
	}
	if got := d.count(t, `SELECT count(*) FROM receipts WHERE status = 'pending'`); got != 0 {
		t.Fatalf("setup left %d interrupted receipts pending", got)
	}
}

// A password file others can read is refused before any connection, in one line that names its mode and not
// what it holds.
func TestAPasswordFileOthersCanReadIsRefused(t *testing.T) {
	d := stdioDemo(t)
	file := filepath.Join(t.TempDir(), "pgpass")
	if err := os.WriteFile(file, []byte("*:*:*:*:secret-in-the-file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PGPASSFILE", file)
	t.Setenv("PGPASSWORD", "")
	t.Setenv("GTM_DATABASE_URL", withoutPassword(t, d.appURL))
	var out, errs bytes.Buffer
	code := mcpCommand(ctx, []string{"stdio", "--scope", "crm:read"}, screen{out: &out, err: &errs, json: true}, strings.NewReader(""))
	var refused struct{ Error struct{ Message string } }
	json.Unmarshal(errs.Bytes(), &refused)
	if code != 1 || refused.Error.Message != "the password file "+file+" has mode 0644; no one but its owner may have access to it, so make it 0600" ||
		strings.Contains(errs.String(), "secret-in-the-file") {
		t.Fatalf("exit %d %s", code, errs.String())
	}
	if err := os.Chmod(file, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkPassfile(withoutPassword(t, d.appURL)); err != nil {
		t.Fatalf("a private file: %v", err)
	}
	if err := os.Chmod(file, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkPassfile(d.appURL); err != nil {
		t.Fatalf("a URL with its password reads no file: %v", err)
	}
}

// withoutPassword is a database URL with its password taken out.
func withoutPassword(t *testing.T, databaseURL string) string {
	t.Helper()
	return strings.Replace(databaseURL, ":app-password-for-this-test@", "@", 1)
}

// GTM_PUBLIC_ORIGIN with a user name or password is refused for every command, and never repeated.
func TestAnOriginWithCredentialsIsRefused(t *testing.T) {
	d := stdioDemo(t)
	t.Setenv("GTM_PUBLIC_ORIGIN", "https://operator:origin-secret@127.0.0.1:8443")
	if _, err := loadConfig(); err == nil || err.Error() != "GTM_PUBLIC_ORIGIN must not hold a user name or password" {
		t.Fatalf("loadConfig: %v", err)
	}
	code, stdout, stderr := d.tokenCommand(t, "new", "--scope", "crm:read", "--json")
	if code != 1 || stdout != "" || strings.Contains(stdout+stderr, "origin-secret") {
		t.Fatalf("token new: %d %s%s", code, stdout, stderr)
	}
	if got := d.count(t, `SELECT count(*) FROM machine_tokens`); got != 0 {
		t.Fatalf("%d tokens made", got)
	}
}

// A token that cannot be stored adds no grant, account or principal: issuance is one transaction.
func TestAFailedIssuanceWidensNothing(t *testing.T) {
	d := stdioDemo(t)
	owner := conn(t, d.ownerURL)
	if _, err := owner.Exec(ctx, `
		CREATE FUNCTION refuse_token() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'no token' USING ERRCODE = 'P0001'; END $$;
		CREATE TRIGGER refuse_token BEFORE INSERT ON machine_tokens FOR EACH ROW EXECUTE FUNCTION refuse_token()`); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := d.tokenCommand(t, "new", "--scope", "crm:read", "--json")
	if code != 1 || !strings.Contains(stderr, "PostgreSQL error P0001") {
		t.Fatalf("token new: %d %s", code, stderr)
	}
	for _, table := range []string{"account_grants", "accounts", "machine_tokens"} {
		if got := d.count(t, `SELECT count(*) FROM `+table); got != 0 {
			t.Errorf("%s holds %d rows after a failed issuance", table, got)
		}
	}
	if got := d.count(t, `SELECT count(*) FROM principals WHERE kind = 'machine'`); got != 0 {
		t.Errorf("a failed issuance made %d machine principals", got)
	}
}

// The stdio server reads the workspace's sources through the seam serve's tools use, as they are connected
// when each call starts: once the CRM is removed a call is refused under its receipt and reads nothing, and
// once it is connected again, under a new generation, the next call is served.
func TestStdioReadsTheSourcesAsTheyAreConnectedNow(t *testing.T) {
	d := stdioDemo(t)
	p := startStdio(t, d, "--scope", "crm:read")
	p.send(stdioStart, stdioReady, stdioCall(2, "get_account", `{"account_id":"acc-003"}`))
	p.answer(1)
	if read := p.answer(2); dig(read, "result", "isError") != false {
		t.Fatalf("before the removal %v", read)
	}
	if code, _, stderr := d.read(t, "connect", "demo", "--remove"); code != 0 {
		t.Fatalf("connect demo --remove: %d %s", code, stderr)
	}
	p.send(stdioCall(3, "get_account", `{"account_id":"acc-003"}`))
	if refused := p.answer(3); dig(refused, "result", "isError") != true ||
		!strings.Contains(stringOf(dig(refused, "result", "content")), "crm_not_connected") {
		t.Fatalf("after the removal %v", refused)
	}
	if code, _, stderr := d.read(t, "connect", "demo"); code != 0 {
		t.Fatalf("connect demo: %d %s", code, stderr)
	}
	p.send(stdioCall(4, "get_account", `{"account_id":"acc-003"}`))
	if read := p.answer(4); dig(read, "result", "isError") != false {
		t.Fatalf("after connecting again %v", read)
	}
	p.stop()
	if got := outcomes(stdioReceipts(t, d)); len(got) != 3 || got[0] != "get_account ok 200" || got[2] != "get_account ok 200" ||
		!strings.HasPrefix(got[1], "get_account error") {
		t.Fatalf("receipts %v", got)
	}
}
