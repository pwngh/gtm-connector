# Security review

**Read this before you trust the connector with customer data. It states what the connector
guarantees, names the Go test that proves each guarantee, and says where the guarantees stop.**

The review covers the `gtm-connector` binary on PostgreSQL, run by one operator on one machine. The
guarantees are the seven rules under "Never break these" in [AGENTS.md](AGENTS.md), and each rule has
a section below. No model takes part in anything described here.

> **This review is not production approval.** Real customer data, or a deployment that more than one
> person signs in to, needs a review of its own.

```sh
GTM_TEST_DATABASE_URL='postgres://USER:PASSWORD@127.0.0.1:PORT/DATABASE?sslmode=disable' \
  GTM_REQUIRE_DATABASE=1 go test -count=1 ./...
```

That command runs every test this review names. A test that needs PostgreSQL skips without
`GTM_TEST_DATABASE_URL`, and `GTM_REQUIRE_DATABASE=1` makes it fail instead. Each test is named with its folder: `cmd/gtm-connector`, or a folder under `internal/`.

Tests run at three depths. A `transport/mcp` test named `TestMounted...` sends requests through
`serve`'s handler to the gate, with receipts, tokens and grants on PostgreSQL. A `cmd/gtm-connector`
test runs the commands' own code, on PostgreSQL where it needs a database, and those named
`TestStdioCommand...` run the built binary. The other `transport/mcp` tests cited here run the gate in
memory, with a receipt store and tools made for the test: `TestStdioOversizedCallKeepsAReceipt`,
`TestHandlerRefusesAnUncoveredScope`, `TestATokenWithAScopeOutsideTheReadSetOpensNothing`,
`TestToolsListHasNoWriteTool`, `TestWriteWithAReadGrantIsRefused`, `TestRateLimitCountsScopeDenials`
and `TestOnlyACallPastTheBearerCheckIsCounted`.

## Who holds authority

The connector trusts the machine it runs on and the person who runs it. Everyone else reaches it
through a check: an assistant with a bearer token, and a browser with a signed-in session. When
`serve` signs people in through their company's identity provider, it also trusts that provider to say
who a person is.

```text
assistant ─────── HTTPS, bearer ───▶ ┌──────────────────────┐ ──▶ PostgreSQL, as the app role
browser ───────── HTTPS, cookie ───▶ │ serve: /mcp and /api │ ──▶ the identity provider: discovery, keys, the code
assistant app ─── stdin, stdout ───▶ │ mcp stdio            │ ──▶ Salesforce: an sf login or a token
operator ──────── database URL ────▶ │ every other command  │ ──▶ Slack: the saved sign-in
                                     └──────────────────────┘
```

Three things carry the operator's authority, and whoever holds one holds that part of it:

- **The database URLs.** The app role's URL (`GTM_DATABASE_URL`) is what `serve`, `mcp stdio` and the
  commands connect with. The owner role's URL (`GTM_OWNER_DATABASE_URL`) runs setup, migrations,
  backup and restore. That role owns the tables, so it can switch their triggers off.
- **The credentials folder** (`GTM_CREDENTIALS_DIR`). It holds the TLS key and certificate, the key that
  signs the browser's pre-sessions and sign-in flows, the demo sign-in's password, each workspace's
  Slack sign-in, and the identity provider's client secret when the client is confidential.
- **The source logins.** An `sf` alias's login or a Salesforce token handed to the process, and the
  saved Slack sign-in. The connector reads with whatever they may read, through its fixed queries.

> **Whoever holds the app role's credentials holds every workspace.** Give the app role's URL, and its
> password, only to people who may read every connected workspace.

The owner can also delegate one read path in SQL. Each database has a reporting role that cannot log
in, and a login role the owner grants it to reads the five `reporting` views of that database, for
every workspace in it: deals, cards with their IDs, digests, sizes and seal receipts, the lines of
each card with their contact names, titles and amounts, units without their text, and receipts with
the record references they returned and without their arguments. Such a reader passes no grant check, and its queries leave no receipt, because PostgreSQL
answers them, not the connector. [The API](docs/api.md#read-only-sql) describes the views.

The identity provider holds authority of its own. With `GTM_OIDC_ISSUER` set, its ID token decides who a
person is. Whoever administers the provider can make an account in an allowed domain or tenant, or sign
in as any of its people, and so read what the operator has granted that person. The provider grants
nothing itself: a person it signs in for the first time reads nothing until the operator runs `grant`.

That role cannot change the schema or switch a trigger off. Within the triggers' rules, it reads and
writes rows:

- It reads every table: the raw evidence of every workspace, every card's sealed bytes, and every
  receipt's arguments and lease token.
- With it, the binary issues a token for any connected workspace, with any read scope, and serves
  `mcp stdio` for one. Neither asks for a session or any other sign-in.
- It writes principals, grants, token rows and session rows directly. A token row it writes is a
  working bearer. A session row it writes is accepted for any enabled person whose sign-in `serve`
  offers: a demo person's while the demo sign-in is on, and a provider's person's only for the provider
  and the workspace `serve` signs in. The demo-only rule is on password sign-in, not on sessions.
- It can read a pending receipt's lease token, and so finish that receipt in place of the process
  that started it.
- A workspace's kind does not show that its evidence is invented: a demo workspace may hold imported
  transcripts, which are real files the operator imported.

| Claim | Test |
| --- | --- |
| `token new` and `mcp stdio` need nothing but the app role's URL. | `TestATokenFromTheCommandOpensMCP`, `TestStdioCommandCRMOnlyReadsCRMAndNeverWidens` (`cmd/gtm-connector`) |
| The app role cannot disable a trigger, truncate, delete an account, drop or create a table, or change the record of migrations. `serve`, `demo` and every command that reads or connects a workspace open the database through one function, which refuses a role that owns the tables or is a superuser. | `TestTheAppRoleOnlyTouchesRows` (`storage/postgres`), `TestDemoRunsOnlyAsTheAppRole` (`cmd/gtm-connector`) |
| The connector grants a database's reporting role the five `reporting` views of that database, and nothing else. A role the migration made is refused a `SELECT` on every table of the connector and on another database's views, and cannot log in. No view has a column named for a token, a lease, a password, a secret, a session, a credential, a call's arguments or request, a card's bytes, a unit's body or a binding. What PostgreSQL gives every role, such as the system catalogs, and whether a member login may connect to another database, are PostgreSQL's own privileges, which the connector does not change. | `TestTheReportingRoleReadsOnlyTheViews`, `TestEachDatabaseHasItsOwnReportingRole` (`storage/postgres`) |
| A migration takes a role that already holds the reporting role's name only when it cannot log in, has no password, which the migration must be able to check, has no member, is a member of no role, and has none of the superuser, inherit, create-role, create-database, replication or bypass-row-security attributes. Any other stops the migration with an error that names the role, and no `reporting` schema is made. The test refuses five roles that can do more and takes one made unable to log in or inherit. Four of the five also inherit, as PostgreSQL makes a role by default, so the test does not show each condition on its own. The migration does not look at what such a role already owns or was granted, and takes nothing away from it, so a role that holds anything keeps it: do not reuse one. | `TestAReportingRoleThatCanDoMoreIsRefused` (`storage/postgres`) |
| `serve` listens on a loopback address and refuses any other `GTM_LISTEN`. Only `GTM_CONTAINER_LISTEN`, set for a container's own network, takes another address. | `TestServeListensOnLoopbackOnly` (`cmd/gtm-connector`) |
| `serve` speaks TLS 1.3 only, with the certificate `setup` wrote. | `TestServeSpeaksOnlyTLS13`, `TestServeAnswersMCPOverTLS` (`cmd/gtm-connector`) |
| Outside `/mcp`, `serve` answers only the Hosts it is configured with, which also blocks DNS rebinding: any other Host gets 421, and another allowed Host is sent on to the public origin. `/mcp` admits the public origin's own Host alone (rule 3). | `TestWrongHostIsRefused`, `TestOtherAllowedHostsGoToThePublicOrigin` (`transport/http`), `TestOptionsStarMeetsTheHostCheck` (`cmd/gtm-connector`) |
| The browser signs in two ways: the demo sign-in, with a password the database holds only for a person of a demo workspace, and sign-in through the identity provider, which stores no password. `serve` refuses to start with the demo sign-in on beside a provider that signs in a workspace of real sources. With the demo sign-in off, it signs nobody in and a demo person's session opens nothing. A principal reads nothing of another workspace. | `TestSessionsRotateExpireAndRevoke` (`storage/postgres`), `TestOtherWorkspacesAndMissingThings` (`authz`), `TestServeRefusesSignInSettingsItCannotServe` (`cmd/gtm-connector`), `TestSessionsFollowTheSignInsOffered` (`transport/http`) |

### Tokens

`token new` makes a bearer token for one workspace, prints it once, and stores only its SHA-256. Every
client token of a workspace belongs to one principal, `MCP clients`, and reads the accounts that
principal is granted. `mcp stdio` makes a token of its own for its process. [MCP](docs/mcp.md#tokens)
describes both.

> **Issuing a token widens every client token of its workspace.** Issuing one, or starting
> `mcp stdio`, grants the `MCP clients` principal each account it was never granted: beside the demo
> CRM, every account of the demo book, and beside a real CRM, every account a command has already
> recorded in the workspace. A grant revoked for the principal stays revoked.

| Claim | Test |
| --- | --- |
| A token is printed once and stored as its SHA-256. It carries only the scopes given, from `crm:read`, `calls:read` and `slack:read`, for five minutes, or 1 to 60 with `--minutes`. `--scope crm:write` is refused before anything is issued. | `TestATokenFromTheCommandOpensMCP`, `TestTokenArguments`, `TestClientTokenGrantsAndWorkspaces` (`cmd/gtm-connector`) |
| Beside the demo CRM, issuance grants the principal the 30 accounts of the demo book, and beside Salesforce the accounts the workspace holds, and no other. A revoked grant stays revoked. Issuance is one transaction, so a failed one grants nothing. A disabled principal gets no token and no stdio server. | `TestATokenFromTheCommandOpensMCP`, `TestClientTokenGrantsAndWorkspaces`, `TestAFailedIssuanceWidensNothing`, `TestSalesforceIsServedOverMCP` (`cmd/gtm-connector`) |
| `serve`, started without `--selection`, answers a token in the workspace it was issued in, with that workspace's sources, and receipts its calls there. | `TestClientTokenGrantsAndWorkspaces` (`cmd/gtm-connector`) |
| A revoked token is refused from its next call. | `TestATokenFromTheCommandOpensMCP`, `TestStdioCommandGrantsAndRevocation` (`cmd/gtm-connector`) |
| A token gets 60 tool calls in 60 seconds. A call is counted once the gate's checks of its size, its parsing, its Host and its bearer have passed, whether a later check refuses it or not, and a call those checks refuse is not counted. The next one is refused `rate_limited`: HTTP 429 with a `Retry-After` header over HTTPS, and JSON-RPC code 1002 over stdio. | `TestOnlyACallPastTheBearerCheckIsCounted`, `TestRateLimitCountsScopeDenials`, `TestMountedBadTokensAreRefused` (`transport/mcp`), `TestStdioCommandChargesScopeDenials` (`cmd/gtm-connector`) |
| The token of `mcp stdio` lasts at most two minutes unless the running process renews it. It is revoked when the session ends: at the end of input, when the app closes stdout, and when the app stops reading. | `TestStdioTokenLastsTwoMinutes`, `TestStdioCommandCRMOnlyReadsCRMAndNeverWidens`, `TestStdioClosedStdoutRevokesTheToken`, `TestStdioClientThatStopsReadingRevokesTheToken` (`cmd/gtm-connector`) |

### Sign-in through the identity provider

With `GTM_OIDC_ISSUER` set, `serve` signs the people of one workspace in through their company's identity
provider, with the OpenID Connect authorization code flow and PKCE, as a confidential or a public client.
[Running it](docs/operations.md#sign-in-through-your-identity-provider) says how to set it up. A person
is the issuer and the subject of the provider's ID token, never an email. They start with no grant, and
the operator gives each account with `grant`, under a receipt. A deal is read through a grant on its
account, as a card is. Ops takes no role of its own: Coverage and Activity read through the same grants.

```text
browser ── /api/v1/session/oidc ──▶ serve: flow cookie ──▶ provider: sign-in ──▶ /api/v1/session/oidc/callback
        ──▶ serve: state, code + verifier, ID token, nonce ──▶ session for the person ──▶ the page asked for
```

| Claim | Test |
| --- | --- |
| A sign-in starts at `/api/v1/session/oidc`, which sets a flow cookie (`__Host-`, `Secure`, `HttpOnly`, `SameSite=Lax`) that holds a random flow ID, its expiry and the page to return to, under an HMAC with the session key. The state, the nonce and the PKCE verifier are derived from the flow ID with that key, so the browser holds none of them. The provider gets the verifier's S256 challenge, and only the verifier opens the code. | `TestASignInThroughTheProvider` (`transport/http`), `TestTheCodeOpensOnlyWithItsVerifier` (`oidc`) |
| The callback starts no session, and sends the provider no code, for a missing, expired or foreign flow cookie, a wrong or repeated state, an authorization response without the `iss` its provider adds, and a flow that has signed in once already, even when the provider would take its code twice. A used flow is recorded by its flow ID, and the cookie's parts are read as strict base64, so a cookie respelled in the bits base64 leaves unused is refused too. | `TestSignInsThatAreRefused`, `TestAFlowSignsInOnceHoweverItsCookieIsSpelled` (`transport/http`), `TestDiscoveryRefusals` (`oidc`) |
| The ID token is verified with a key of the provider's key set that fits its algorithm (RS256 to RS512, PS256 to PS512, ES256 to ES512, or EdDSA, which a key or a token may also name Ed25519) and, when the token names one, its key ID. `alg: none`, HS256 keyed with the provider's public key, a critical header and an access token's type are refused, no key is taken from the token, and an RSA key under 2048 bits verifies nothing. | `TestTokensThatDoNotVerify`, `TestEveryAlgorithmOfTheProvidersKeys`, `TestAKeyPublishedAsEd25519VerifiesEdDSA` (`oidc`) |
| Its claims: `iss` is the issuer, `aud` this client and no other, `azp` this client when it is there, `exp` not passed, `iat` at most ten minutes old and `nbf` passed, each with a minute's leeway, `nonce` the flow's own, and `sub` plain text. A claim named twice refuses the token. A token another sign-in received is refused for its nonce. | `TestTokensThatDoNotVerify` (`oidc`), `TestSignInsThatAreRefused` (`transport/http`) |
| The provider's key set is kept for at most an hour, or as long as its `Cache-Control` says, at least a minute. A token under a key the kept set lacks reads the set again, at most once in ten seconds, so a key the provider rotated in signs in, and a key it retired verifies nothing once the set is read again. | `TestKeyRotation` (`oidc`) |
| The discovery document must name the configured issuer, give https endpoints, and take the S256 challenge. A discovery document or a key set that fails to read is not asked for again for five seconds, so sign-ins do not wait on a failing provider one after another. | `TestDiscoveryRefusals`, `TestAFailingProviderIsAskedAgainAfterFiveSeconds` (`oidc`) |
| Only a person the configuration allows signs in: an email the provider verified in an allowed domain, and the tenant claim (`hd` or `tid`) when one is set. `email_verified` counts as true or false, or the text `"true"` or `"false"`; any other value refuses the token. A person refused gets no principal. `serve` refuses a configuration that names neither a domain nor a tenant. | `TestWhoMaySignIn`, `TestEmailVerifiedSentAsText` (`oidc`), `TestSignInsThatAreRefused` (`transport/http`), `TestServeRefusesSignInSettingsItCannotServe` (`cmd/gtm-connector`) |
| A person is found by issuer and subject: another subject with the same email is another person. A new person reads nothing, and with a grant on one account reads that account alone, until it is revoked. | `TestASignInThroughTheProvider` (`transport/http`), `TestGrantsThroughTheCommandOpenAccountsInTheBrowser` (`cmd/gtm-connector`) |
| Ops is read through the same grants: the account list, an account's cards, a card, a receipt and the deals hold the accounts a person is granted and no other, and a person granted every account reads the whole book. | `TestOpsReadsOnlyWhatAPersonIsGranted` (`transport/http`) |
| A sign-in returns only to a page of the web app; any other address returns to `/`. | `TestASignInReturnsOnlyToThisApp` (`transport/http`) |
| A session from the provider lasts 60 minutes, or `GTM_OIDC_SESSION_MINUTES`, from 5 to 720. It opens nothing once `serve` no longer signs in that provider's workspace. A sign-in ends the session the browser held. | `TestSessionsFollowTheSignInsOffered`, `TestSigningOutOffersTheProvidersSignOut` (`transport/http`) |
| `serve`'s surface itself refuses the demo sign-in beside a provider that signs in a workspace of real sources, as `serve` does before it listens. | `TestTheDemoSignInIsRefusedBesideAProviderOfALiveWorkspace` (`transport/http`), `TestServeRefusesSignInSettingsItCannotServe` (`cmd/gtm-connector`) |
| A sign-out ends the session at the connector and, for a person of the provider, offers the provider's own sign-out page when the provider publishes one. It leaves the provider's own session as it is. | `TestSigningOutOffersTheProvidersSignOut` (`transport/http`), `TestTheEndSessionAddressIsTheProvidersForThisClient` (`oidc`) |
| A sign-in, kept or refused, gets no receipt. It is one line of the log of `serve`, by the principal and the workspace, or by the reason it was refused, with no token, code, secret or email. | `TestASignInThroughTheProvider`, `TestSignInsThatAreRefused` (`transport/http`) |
| `grant` gives or revokes one person's grant on one account, and `grant --disable` ends a person's sessions at once and refuses their sign-ins, with their grants kept, until `grant --enable`. Each change runs under a receipt committed before it, the change commits with that receipt's finish, and a person or an account the workspace does not have keeps its receipt, finished as an error. | `TestGrantsThroughTheCommandOpenAccountsInTheBrowser`, `TestDisablingAPersonEndsTheirSessionsAtOnce` (`cmd/gtm-connector`) |
| The grant commands name only people of the issuer `serve` signs in through, `GTM_OIDC_ISSUER`: `--to` by an email that issuer verified, `--subject` by subject. A subject given as `--to`, an email the provider did not verify, and a person of another issuer name nobody, and an email two people share is refused with the subjects it matched. | `TestGrantNamesOnlyAPersonOfServesIssuer`, `TestGrantsThroughTheCommandOpenAccountsInTheBrowser` (`cmd/gtm-connector`) |

## 1. A receipt comes before every tool call

**The gate writes one outer receipt per recognized call, committed before the call is checked or run,
and finished before anything is released.**

```text
tools/call ──▶ receipt committed (pending) ──▶ checks ──▶ the tool reads ──▶ receipt finished ──▶ answer
                                                  └──▶ refused ──▶ receipt finished as an error ──▶ refusal
```

A call is recognized when its body names `tools/call`, or its `Mcp-Method` header does. A body nested
too deep to parse is read for its method without descending, and a stdio line past 4 MiB by its start.
A request that is not recognized gets no receipt: one that calls no tool, such as `tools/list`, and a
body that fails as it is read with no header to mark it.

Two steps come before the receipt's insert. `serve` looks up which workspace a bearer was issued in, to
pick that workspace's gate. The gate then looks up who holds the bearer, and opens the workspace's
sources from their rows. A database failure in the second is answered as a receipt that could not be
committed. After the insert, every check runs, so a call refused for its token, its scope, its arguments
or its Host is receipted.

> **One refusal keeps no receipt.** When `serve` cannot read which workspace a bearer was issued in,
> because the database fails or takes more than five seconds, no gate can take the call, since a gate
> keeps the receipts of its own workspace. The call is answered as one whose receipt could not be
> committed, HTTP 503 `audit_unavailable` in the gate's JSON, nothing runs, and `serve` logs one fixed
> line with the failure's code. This is the only place a call is refused with no receipt. The
database holds each receipt to its rules with triggers, so a writer that goes around the connector's
code meets the same rules. [Audit](docs/audit.md) describes what a receipt holds.

> **No receipt, no call.** When the receipt cannot be committed, nothing runs and the answer is HTTP
> 503 `audit_unavailable`. When it cannot be finished, nothing is released.

| Claim | Test |
| --- | --- |
| Every recognized tool call, over HTTPS and over stdio, gets one outer receipt. | `TestMountedCallsLeaveOneOuterReceipt` (`transport/mcp`), `TestStdioCommandCRMOnlyReadsCRMAndNeverWidens` (`cmd/gtm-connector`) |
| Every source read starts after its pending receipt is committed, as a second connection to the database sees it, over HTTPS and over stdio. | `TestMountedCallsLeaveOneOuterReceipt`, `TestMountedStuckReceiptsOverStdio` (`transport/mcp`) |
| A refused call keeps its receipt, finished as an error with no rows, and reads nothing: a bad token, a missing scope, the write tool, invalid arguments, an unknown tool, an account outside the grant or the selection, the wrong Host, an `Origin` header, a recognized call whose body cannot be parsed or arrives cut off, arguments that hold a NUL, and a request that repeats its own credential. | `TestMountedCallsLeaveOneOuterReceipt`, `TestMountedBadTokensAreRefused`, `TestMountedGrantsLimitEveryTool`, `TestMountedHostAndOriginChecks`, `TestMountedEveryRefusedCallLeavesItsReceipt`, `TestMountedARequestThatRepeatsItsCredential` (`transport/mcp`), `TestStdioCommandMalformedCallsStillLeaveReceipts` (`cmd/gtm-connector`) |
| A stdio line past 4 MiB that starts as a tool call keeps one error receipt under its tool's name. This is tested in memory. | `TestStdioOversizedCallKeepsAReceipt` (`transport/mcp`) |
| A body that fails as it is read, with no header that marks it as a call, gets no receipt, and neither does a request that calls no tool. | `TestMountedEveryRefusedCallLeavesItsReceipt`, `TestMountedCallsLeaveOneOuterReceipt` (`transport/mcp`) |
| With the receipt store down at the start, nothing runs and the answer is 503 `audit_unavailable`. Down at the finish, nothing is released, not even a refusal's own words, and the receipt stays pending. | `TestMountedCallsLeaveOneOuterReceipt` (`transport/mcp`) |
| A receipt's start or finish waits on a lock no longer than a fixed limit. A stuck database refuses the call, or withholds its answer, at the gate's own deadline. | `TestMountedStuckReceiptsOverHTTPS`, `TestMountedStuckReceiptsOverStdio` (`transport/mcp`), `TestAReceiptStartOrFinishGivesUpOnALock` (`storage/postgres`) |
| A caller who leaves is answered nothing, nothing is read after it left, and its receipt is finished as an error. A brief whose publication has begun completes and is sealed. | `TestMountedACallerWhoLeaves` (`transport/mcp`) |
| A receipt left pending by a process that stopped is finished as error/503 `interrupted` once its lease has expired by the database's clock. `setup`, `mcp stdio` and `serve` do this as they start. | `TestExpiredLeaseCannotFinish` (`storage/postgres`), `TestStdioAndSetupFinishInterruptedReceipts`, `TestServeFinishesInterruptedReceiptsAsItStarts` (`cmd/gtm-connector`) |
| For any writer, a receipt finishes once, its arguments never change, an error holds no rows, a parent finishes after its children, and receipts are never deleted or truncated. A finish must present the receipt's live lease token: the database refuses a missing, wrong or expired one. | `TestReceiptFinishesOnce`, `TestExpiredLeaseCannotFinish`, `TestTheLeaseHolderIsEnforcedByTheDatabase`, `TestParentsAndChildren`, `TestArgumentsKeepTheirOrder` (`storage/postgres`) |
| The commands read under receipts too: a card's reads, and `accounts` and `inspect` on a real CRM. With no receipt store, nothing is read, and an owner read is never left pending. | `TestReceiptsRuleOnPostgres` (`cards`), `TestSalesforceIsReadByEveryCommand`, `TestAnOwnerReadIsNeverLeftPending` (`cmd/gtm-connector`) |
| Each request a command makes to a source outside a read runs under a receipt of its own, committed before the request, in the workspace the command names, which is made for real sources if it does not exist. In `connect salesforce` that is each `sf` command (`connect_salesforce_login_display`, `connect_salesforce_login_token`) and each request to the org (`connect_salesforce_org`, `connect_salesforce_userinfo`, and `connect_salesforce_describe_account` and the other objects). In `debug slack connect`, `status --check` and `disconnect --revoke` it is Slack's code exchange, refresh, identity check and revocation (`slack_sign_in_exchange`, `slack_sign_in_refresh`, `slack_sign_in_identity`, `slack_sign_in_revoke`). A receipt is finished with what the checks of the answer decide, so an answer that is refused once it is read, such as an exchange that says ok and holds no grant, finishes its receipt as an error. A request that fails, is refused or is cancelled leaves its own receipt as an error, and connects nothing. | `TestEachSalesforceConnectRequestHasItsReceiptFirst`, `TestSlackSignInRequestsRunUnderReceipts`, `TestASlackSignInChangesNothingBeforeItsReceipt`, `TestConnectSalesforceRefusesWhatItCannotPin`, `TestACancelEndsTheSFProcessGroup`, `TestSalesforceIsReadByEveryCommand` (`cmd/gtm-connector`) |
| A Slack refresh's receipt is committed before the saved sign-in is marked as needing a login, so a receipt store that refuses leaves the sign-in as it was, and Slack is sent nothing. | `TestASlackSignInChangesNothingBeforeItsReceipt` (`cmd/gtm-connector`) |
| When `serve` cannot read which workspace a bearer was issued in, the call is answered 503 `audit_unavailable` in the gate's JSON, nothing runs and no receipt is kept, and `serve` logs one fixed line that holds neither the bearer nor PostgreSQL's text. | `TestABearerWhoseWorkspaceCannotBeReadIsRefusedInTheGatesWords` (`cmd/gtm-connector`) |
| A connection the network loses with its socket open does not keep its transaction's locks: the server ends a session that waits 30 seconds inside a transaction, and both ends of every pooled connection send TCP keepalives after 30 seconds of silence, every 10 seconds, and give up after 3. | `TestTheServerEndsATransactionLeftIdle` (`storage/postgres`) |

The same limit ends a transaction whose own process pauses for 30 seconds between two of its statements.
The connector's transactions make no request to a source and wait on nothing but the database: a
publication runs its checks in the process between statements that wait at most 5 seconds for a lock
and run at most 10, and a backup or a restore keeps its session busy while a table is copied. So only a
stalled process, not a slow source or a slow lock, meets the limit, and its transaction is then rolled
back and nothing of it is kept.

The lease keeps a process whose lease expired and was taken over, or that never held it, from
finishing a receipt. It does not keep out a holder of the app role, who can read the token.

## 2. A scope opens only its own source

**A token reads a source only with that source's scope, and an account only through its principal's
live grant. A selection narrows the accounts further. No MCP tool writes to a source.**

The gate checks the scope before the tool runs, and the tool's handler checks it again. The tool then
checks the selection, when there is one, and the grant, before it reads anything. A search or a list
asks the source only for accounts that pass both. [MCP](docs/mcp.md#tools-and-scopes) lists each tool
and its scope. The one tool that writes, `prepare_renewal_brief`, saves its brief in the connector's
own database.

| Claim | Test |
| --- | --- |
| A token reads only the source its scope opens. Every other tool answers 403 `insufficient_scope`, naming the scope it needs, and its source is not read. | `TestMountedScopeOpensOnlyItsSource` (`transport/mcp`), `TestStdioCommandCRMOnlyReadsCRMAndNeverWidens` (`cmd/gtm-connector`) |
| The tool's handler refuses a call whose admitted scopes do not cover its tool, before the tool runs. | `TestHandlerRefusesAnUncoveredScope` (`transport/mcp`) |
| A token with any scope outside the three read scopes opens nothing. It is refused whole, never trimmed, and the database stores no other scope. | `TestATokenWithAScopeOutsideTheReadSetOpensNothing` (`transport/mcp`), `TestTheReadScopesMigrationRefusesAWriteScope` (`storage/postgres`) |
| No MCP tool writes to a source. `tools/list` names no write tool, and a call of `log_activity` is refused 403 `crm_write_disabled` under its receipt. | `TestToolsListHasNoWriteTool`, `TestWriteWithAReadGrantIsRefused`, `TestMountedCallsLeaveOneOuterReceipt` (`transport/mcp`) |
| An account outside the principal's live grants is refused before any read, and left out of searches and lists. A revoked grant stops a saved brief from being released. | `TestMountedGrantsLimitEveryTool` (`transport/mcp`), `TestStdioCommandGrantsAndRevocation` (`cmd/gtm-connector`) |
| A Salesforce search puts the words of its text, each as literal text, into a fixed query, with a `%` of its own between them, and the accounts the caller may read into the query itself. | `TestSearchesAreFixedQueries` (`sources/salesforce`) |
| A Slack read asks for messages only with a GET of `conversations.replies` for the account's mapped thread. | `TestOneRequestAndTheMessagesAsGiven` (`sources/slack`) |
| With a selection, an unselected account is refused before any read, Slack is read only where the selection includes it, searches and lists hold selected accounts only, and the two brief tools are served. Without one, they are not. | `TestMountedSelectionLimitsAccounts` (`transport/mcp`), `TestStdioCommandWithASelection` (`cmd/gtm-connector`) |
| A selection file is a regular file, not a symbolic link, that no one but its owner may write, at most 65,536 bytes, in one shape. An empty `--selection` is refused, never read as no selection. | `TestLoadSelection`, `TestServeSelectionFlag`, `TestMCPArgumentsNeedNoDatabase` (`cmd/gtm-connector`) |
| A prepared brief checks the bearer again before each read inside it and once more before it publishes, counts each read against the bearer's rate, and goes by the scopes the bearer carries at each check. A bearer that expires before the last check leaves no brief. | `TestMountedReadsInsideABriefCheckTheBearerAgain`, `TestMountedABriefGoesByTheScopesTheBearerCarriesNow` (`transport/mcp`) |
| The two deal tools read no source: a workspace whose CRM cannot be opened still lists its deals and gives their cards. `list_deals` holds the deals of the accounts the principal is granted, and with a selection only those of selected accounts. | `TestMountedDealToolsOpenNoSource`, `TestMountedDealCard` (`transport/mcp`) |
| `read_deal_card` checks the principal's grant on the deal's account when it reads the deal and again when it reads the card. Before it releases a card it also requires the bearer's scopes to cover every source the card read, and, when a selection is served, the selection to hold the account, and the account's Slack when the card read Slack, and the card to have been read from the sources the workspace is bound to now. As the card is released it verifies the bearer once more: a bearer that no longer verifies, or no longer carries a scope the card needs, gets no card. It does not check the grant again at release, so a grant revoked after the card is read does not stop that one answer. | `TestMountedDealCard`, `TestMountedDealCardRefusals`, `TestMountedDealCardRereadsTheBearer`, `TestMountedDealCardFromOtherSources` (`transport/mcp`) |
| An unknown deal, a deal of an account the principal is not granted, and a deal outside the selection get the same reply, which says nothing more of the deal, and each refusal leaves a receipt finished as an error with no rows. The receipt of a call for a granted, known deal names the deal's account, and the receipt of a call for an unknown deal names none. | `TestMountedDealCardRefusals`, `TestMountedDealCardReceiptNamesTheAccount` (`transport/mcp`) |
| A card is published only if its principal still holds a grant on the card's own account, checked in the publication's transaction. | `TestPublicationRechecksTheGrant`, `TestPublicationRechecksTheCardsOwnGrant` (`storage/postgres`) |

In the browser, a session reads a card, a unit or a receipt only through a grant on the card's account.

| Claim | Test |
| --- | --- |
| Every evidence read needs a live session and a grant. A grant on one account opens no other, a revoked grant reads nothing, and an ID that does not exist is refused like one not granted. | `TestEvidenceReadsNeedAGrant`, `TestAGrantOnOneAccountOpensNoOtherOverHTTP` (`transport/http`), `TestGrantsGuardEveryUnitAndReceiptRead` (`authz`), `TestRefusalsAreUniform` (`storage/postgres`) |
| The deal routes read through the same grants. The deal list holds the deals of the granted accounts only, and a deal of another account answers 403 as an unknown one does. Registering a deal for an account without a grant is refused 403 and leaves one more `get_account` receipt finished as error/403 with no rows, and no `list_opportunities` receipt. After a grant is revoked, the list leaves the account's deals out and a read of one answers 403. | `TestDealRoutesKeepTheGrants` (`cmd/gtm-connector`) |
| A refresh reads as the signed-in person and publishes a card for the deal: every read the card rests on, and its seal, name them. It needs the CSRF token and the public origin. A person with no grant on the deal's account, an unknown deal and what is no deal ID are each refused 403 under one `get_account` receipt finished as error/403 with no rows, and no card is published for any deal; a closed deal is refused 409. | `TestRefreshPublishesANewCardForTheDeal` (`cmd/gtm-connector`) |
| A refreshed card is the deal's it was asked for or is not published: a deal that closed before the read, one that closes during it, and one that gives way to the account's next deal during it, each publish nothing. | `TestARefreshedCardIsOnlyTheDealsAskedFor` (`cmd/gtm-connector`) |
| The match routes read through the same grants. The queue holds the matches of the granted accounts only. A decision needs the CSRF token and the public origin. A decision that is kept leaves a `decide_match` receipt. A person with no grant on the match's account is refused 403 as for an unknown match, in the same words, and each of those refusals leaves a receipt. A body that is not one of the two documented objects, a deal of another account and a superseded match are refused 400, 400 and 409. A decision sent after the grant was revoked is refused 403. | `TestMatchRoutesKeepTheGrants` (`cmd/gtm-connector`) |
| No answer under the API, MCP, discovery or health paths may be kept by a browser or a proxy: not the session, a card, its bytes, a unit or a receipt. | `TestNoAPIResponseIsKept`, `TestNoSignedInResponseIsKept` (`transport/http`) |

## 3. A bad or expired token is refused, never retried

**A token that does not verify gets one refusal in fixed words, and nothing is tried in its place.**

| Claim | Test |
| --- | --- |
| A bearer that is missing, malformed, unknown, expired, revoked, of a disabled principal, limited to a selection or of another workspace than the gate's, two `Authorization` headers, and a session cookie in a bearer's place are each refused 401 `invalid_token` in the same words. Each refused call leaves one receipt, reads nothing and charges no rate. | `TestMountedBadTokensAreRefused` (`transport/mcp`) |
| The database's clock decides expiry: a process whose clock is behind still refuses a token the database says has expired. | `TestMountedExpiryIsTheDatabasesClock` (`transport/mcp`) |
| Once the token of `mcp stdio` has expired, calls and listings are refused, nothing is retried with another token, and the expired token is never renewed. | `TestStdioCommandExpiredTokenIsRefusedNotRetried` (`cmd/gtm-connector`) |
| The browser API takes its session cookie only. A bearer, alone or beside the cookie, is refused, and the cookie opens nothing at `/mcp`. | `TestCookiesAndBearerTokensNeverCross` (`transport/http`), `TestMountedBadTokensAreRefused` (`transport/mcp`) |
| The session cookie is `__Host-` named, `Secure`, `HttpOnly` and `SameSite=Lax`. Sign-in rotates the session and its CSRF token, a pre-session signs in once, also under a cookie respelled in the bits base64 leaves unused, and a session expires, can be revoked, and ends when its principal is disabled. | `TestSignInRotatesTheSessionAndCSRF`, `TestConcurrentSignInsFromOnePreSession`, `TestAPreSessionSignsInOnceHoweverItsCookieIsSpelled`, `TestSessionsExpireAndCanBeRevoked`, `TestADisabledPrincipalIsSignedOut` (`transport/http`) |
| Every browser write needs one `Origin` equal to the public origin and the session's own CSRF token. | `TestWritesNeedTheConfiguredOrigin`, `TestEveryWriteNeedsItsCSRFToken` (`transport/http`) |
| `/mcp` admits only the public origin's own Host, over TLS, with no `Origin` header. | `TestMountedHostAndOriginChecks` (`transport/mcp`) |
| Salesforce: the first HTTP 401 in a read asks for the login once more, and the read goes on only with a login of the same org and user. Any other login is refused, and nothing is read with it. | `TestAnExpiredTokenAsksSFOnce`, `TestAnExpiredTokenReadsTheEnvironmentOnce`, `TestALoginReadsAsOneUser`, `TestATokenForAnotherOrgThanTheAliasNamesIsRefused` (`sources/salesforce`) |
| Slack: a read refreshes once after Slack says the token expired, and a refused token never replaces a newer sign-in. | `TestOneRefreshAfterSlackSaysTheTokenExpired`, `TestARefusedTokenNeverReplacesANewerSignIn` (`sources/slack`) |

## 4. The connector writes every character of a card

**Every figure and quotation on a card carries a number that resolves to a stored record version and
the receipt of the read that bound it. The database refuses a card that breaks this, from any writer.**

A card is sealed when it is published: its seal is a receipt that names the SHA-256 of the card's
bytes. Publication checks every unit against the read it cites, in one transaction with the card.
[Deal cards](docs/cards.md) explains how a card is built and read.

| Claim | Test |
| --- | --- |
| The card maker writes a card from its evidence alone: it builds every one of the 450 recorded cards from its inputs, each verifies against its seal and its read receipts, and each of the 90 golden cards is its recorded bytes, with the same units, gaps and seal. | `TestCardsFromInputs` (`cards`) |
| The database refuses a card whose seal does not name its bytes, whose unit and gap rows differ from its bytes, or whose units cite a read that did not finish ok, a read of another account or brief, a read whose source has moved, or a record version that read did not bind. | `TestCardSealMustNameItsBytes`, `TestTheTriggersHoldTheCardRules`, `TestACardHoldsExactlyTheRowsItCounts`, `TestPublishChecksEveryUnitsProvenance` (`storage/postgres`) |
| A card, its units, gaps and lines, and every record version and binding are never changed or deleted once written. | `TestDurableRowsRefuseChange` (`storage/postgres`) |
| Evidence that fails a check gives no card: a refused evidence vector, a live thread that is not its snapshot, another workspace's transcript, and a read whose source moved. | `TestEvidenceVectors` (`evidence`), `TestAThreadThatFailsItsSnapshotGivesNoCard`, `TestAnotherWorkspacesTranscriptGivesNoCardOnPostgres`, `TestReceiptsRuleOnPostgres` (`cards`) |
| The verifier `inspect` runs refuses a changed byte, a value written another way and a seal that does not name the bytes. Run over the recorded card folders, it also refuses a read whose receipt-log row was changed in any one field but its audit ID. | `TestVerifyRefuses`, `TestVerifyComparesEveryColumnOfEveryRead` (`cards`) |
| A restore loads its rows with the tables' triggers off, then checks them against the rules those triggers hold: the receipts and their bindings, the cards, their units, gaps and lines, and no imported calls connected in a workspace where a read returned a demo call note. It refuses an archive that breaks one, also when its digests were computed again. | `TestRestoreRefusesEditedArchives` (`cmd/gtm-connector`), `TestRestoreRefusesRowsTheWritePathRefuses`, `TestRestoreRefusesLinesThePublicationRefuses`, `TestARestoreRefusesImportsBesideReadDemoCallNotes` (`storage/postgres`) |

## 5. Source text is data

**Text from a source is neutralized before the connector renders it, and no MCP tool writes to a
source, so nothing a record says can make the connector write to one.**

Neutralizing turns Slack mentions, links, control and format characters, and bracketed numbers into
inert words. A link shows the host a browser would open, or `[link unknown host]`. A quotation is one
whole sentence with its speaker's role, which comes from a label and never from wording.

| Claim | Test |
| --- | --- |
| Source text is neutralized as a recorded corpus of it was: mentions, links in every form, control characters and bracketed numbers become inert words. A host a browser would rewrite or refuse is not shown. | `TestSafeTextCorpus`, `TestLinkHost`, `TestAmbiguousLinkHost`, `TestMarkdownLinkWithAnyScheme`, `TestHostABrowserRewritesOrRefuses` (`evidence`) |
| A bare mail address in quotation text becomes `[link mail]`, as one in angle brackets does, since a client may link either, also one whose domain starts with `www.`. A `]` right before a `(` in a quotation gets a space between, so no quotation, and no marker beside a `(`, is a Markdown link: of random text, no quotation holds `](` or a bare address. A contact's email, a record value the connector writes, is shown as the CRM holds it. A unit sealed under neutralizer version 4 is shown with its address replaced. | `TestABareMailAddressInAQuotationIsNeutralized`, `TestNoQuotationIsALinkOrABareAddress`, `TestACardQuotesNoBareAddressAndKeepsTheContactsEmail`, `TestAUnitOfVersion4ShowsNoBareAddress` (`evidence`) |
| A Slack message's `&amp;`, `&lt;` and `&gt;` are read back once, before the text is neutralized: a link or a mention typed by hand is replaced as Slack's own is, `&amp;lt;` is read as `&lt;`, a `>` that opens a line is read as a quote marker and left out, and a tag typed inside a line, such as `<b>`, is in the sentence as text. A call note's `&amp;` is not read back. Each sentence is neutralized once more as it stands on the card, so a reference definition that only an indent or a quote marker hid shows `[link unknown host]`, in a Slack message and in a call note. | `TestASlackMessageIsReadAsItsAuthorTypedIt` (`evidence`) |
| A unit sealed under an older neutralizer version is shown under today's rules, its quotation and speaker neutralized again, by `inspect`, the CLI's card, the web API and the brief tools. Its card's bytes stay as sealed and still verify. | `TestAnOlderCardsTextIsRenderedAgain` (`evidence`), `TestACardSealedUnderOlderRulesVerifiesAndShows`, `TestTheCLIPrintsAnOlderCardRenderedAgain` (`cmd/gtm-connector`), `TestAUnitSavedUnderOlderRulesIsShownUnderTodays` (`transport/http`), `TestABriefToolShowsAnOlderUnitUnderTodaysRules` (`mcptools`) |
| The browser's account names, the receipt arguments `inspect` shows and the names of members in printed JSON are neutralized too, and `--json` writes format characters as escapes. | `TestAccountsListThePrincipalsGrants` (`transport/http`), `TestInspectShowsAReceiptsArgumentsInert`, `TestNeutralMakesMemberNamesInert`, `TestFormatCharactersAreEscapedInJSON` (`cmd/gtm-connector`) |
| A quotation is a whole sentence: one an excerpt's edge cuts is kept as a unit marked partial and never quoted in the card's text, and a figure inside a sentence stays there and never becomes a record value. | `TestCardsFromInputs` (`cards`, its ten invented cases), `TestExcerptEdgeCutsASentence` (`evidence`) |
| A speaker's role comes from a label, never from wording: a seller's retelling stays the seller's, a name a seller and a contact share is unknown, and a stated name is the only label of its cue. | `TestCardsFromInputs` (`cards`, its ten invented cases), `TestAStatedNameIsTheOnlyLabelOfACue` (`evidence`), `TestAVoiceTagAfterAStatedNameIsRefused` (`sources/calls`) |
| Every response of the web app carries a Content-Security-Policy that names no other origin and allows no inline script but the theme script, which it names by its hash. | `TestEveryDocumentIsUnderThePolicy` (`transport/http`) |

### Where source text shows

Rendered surfaces show the card as the connector wrote it, neutralized. Export and diagnostic surfaces
show source text as it is stored, for checking a seal or tracing a read.

| Surface | What it shows | Who can reach it | Test |
| --- | --- | --- | --- |
| The card's text: `brief`, `GET /api/v1/cards/{card}/text`, the web app | Rendered and neutralized | The app role's URL; a session with a grant on the card's account | `TestCardsFromInputs` (`cards`), `TestACardServesItsLinesAndItsText` (`transport/http`) |
| Units, gaps and lines: `inspect BRIEF_ID`, `GET /api/v1/cards/{card}`, the `text` and `units` of the two brief tools | Rendered and neutralized, as sealed | The app role's URL; a session with a grant; a token with `crm:read`, a grant and a selection | `TestReadmeShowsWhatTheInspectCommandPrints` (`cmd/gtm-connector`), `TestEvidenceReadsNeedAGrant` (`transport/http`) |
| `accounts`, `inspect ACCOUNT_ID`, `inspect RECEIPT_ID`, `GET /api/v1/accounts` | Neutralized | The app role's URL; a session | `TestInspectShowsAReceiptsArgumentsInert`, `TestNeutralMakesMemberNamesInert` (`cmd/gtm-connector`), `TestAccountsListThePrincipalsGrants` (`transport/http`) |
| `GET /api/v1/cards/{card}/bytes` | Export: the whole sealed artifact, its `evidence.records` unneutralized. The web app fetches it for each card it opens, to check the seal, and drops `evidence` as it parses it. | A session with a grant on the card's account | `TestEvidenceReadsNeedAGrant` (`transport/http`) |
| `GET /api/v1/receipts/{receipt}`, and the receipt in `GET /api/v1/cards/{card}/units/{n}` | Diagnostic: a receipt's arguments and request ID as stored | A session with a grant on the receipt's account | `TestEvidenceReadsNeedAGrant` (`transport/http`) |
| `audit --json` | Diagnostic: every receipt's arguments and request ID as stored, written in ASCII with escapes | The app role's URL | `TestAuditFilters` (`cmd/gtm-connector`) |
| `debug calls inspect --full` | Diagnostic: the original transcript, quoted in text and escaped in JSON | The app role's URL | `TestOnlyFullInspectionShowsTextOutsideTheExcerpt` (`storage/postgres`), `TestFormatCharactersAreEscapedInJSON` (`cmd/gtm-connector`) |
| `list_deals` | Each deal's record, with its account name neutralized and its newest card's ID and time. No card. Marked `untrusted_data: true`. | A token with `crm:read` and a grant on the deal's account, within the selection when one is served | `TestMountedDealCard` (`transport/mcp`), for the records it lists |
| `read_deal_card` | The deal's record and its newest card: the text and the units as `inspect` shows them, which is as sealed for a card of today's rules and rendered again for a card that holds a unit sealed under older ones, and the lines and gaps as saved. Marked `untrusted_data: true`. | A token with `crm:read` and the scopes of every source the card read, a grant on the deal's account, and with a selection the account, and its Slack when the card read Slack | `TestMountedDealCard`, `TestMountedDealCardRefusals` (`transport/mcp`), for the sealed text of a card of today's rules, its seal's digest and receipt, and the record references |
| `GET /v1/deals`, `GET /v1/deals/{id}`, `GET /v1/matches` with or without `all=1`, and the answers of `POST /v1/deals` and `POST /v1/matches/{id}` | Each deal's and match's account name neutralized, and for one deal its newest card as `GET /api/v1/cards/{card}` gives it, with the matches that card read. No source scope or selection applies in the browser. | A session with a grant on the account | `TestDealRoutesKeepTheGrants`, `TestMatchRoutesKeepTheGrants` (`cmd/gtm-connector`), for who can reach them. No test requests `all=1`. |
| The `reporting` views | Export: what a sealed card already prints of a source, as a line's text, label and contact, and each receipt's record references. No unit's body, no card's bytes, no call's arguments. | A login role granted the database's reporting role | `TestTheReportingRoleReadsOnlyTheViews` (`storage/postgres`) |
| The five direct MCP reads | Data for the client: each record as its source adapter returned it, marked `untrusted_data: true` (see the [limits](#known-limits)) | A token with the tool's scope and a grant on the account | `TestMountedDirectReadsReturnTheRecordsAsTheSourceGaveThem` (`transport/mcp`), `TestOneRequestAndTheMessagesAsGiven` (`sources/slack`) |
| A backup | Export: every row and the credential files `setup` makes | Whoever can read the file | `TestABackupRestoresEveryRow` (`storage/postgres`), `TestBackupAndRestoreKeepState` (`cmd/gtm-connector`) |

Whoever holds the app role's URL also reads all of it straight from the tables, as
[who holds authority](#who-holds-authority) says.

## 6. Real sources are opt-in and never fall back to demo data

**A workspace is made for the invented demo book or for real sources, and keeps that. A real source is
connected by hand, and one that is missing or failing is refused by name.**

Each source of a workspace is one row. It names the kind of source, what the operator chose, and the
digest of the identity it is pinned to, and it holds no token, address or org ID. Its generation moves
on every change. A read records the generation it started under and is refused if the row has moved
since, so a source detached or connected again is never read through what was opened before.

| Claim | Test |
| --- | --- |
| For any writer, the database refuses demo data in a workspace of real sources and a real CRM or Slack in a demo workspace. Imported transcripts may sit beside the demo CRM, never in a workspace that has read demo call notes. | `TestUpdateSourcesSavesAChangeAndFencesIt`, `TestDemoCallNotesAndImportsNeverShareAWorkspace` (`storage/postgres`), `TestDebugCallsBesideTheDemoCRM` (`cmd/gtm-connector`) |
| A source that is not connected, removed or refused is refused by name and never read as demo data. An account with nothing imported gets a card that says so. | `TestReadCommandsRefuseWhatIsNotConnected`, `TestConnectRefusesWhatThisBuildDoesNotConnect`, `TestSalesforceIsReadByEveryCommand`, `TestSlackCommandsRefuse`, `TestImportedTranscriptsMakeTheRecordedCards` (`cmd/gtm-connector`) |
| `serve` and `mcp stdio` read the sources as connected when each call starts: a source removed while they run is refused, and read again once connected again. | `TestServeFollowsWhatTheWorkspaceIsConnectedTo`, `TestStdioReadsTheSourcesAsTheyAreConnectedNow` (`cmd/gtm-connector`) |
| A source stays pinned to the identity it was first connected to, also after it is removed. Another org or Slack user is refused, and the database refuses a changed pin from any writer. | `TestUpdateSourcesSavesAChangeAndFencesIt` (`storage/postgres`), `TestAWorkspaceKeepsTheCRMItWasPinnedTo`, `TestAPinnedWorkspaceRefusesASignInOfAnotherUser` (`cmd/gtm-connector`) |
| A transcript import is saved only under the workspace's own finished read that returned its account. An import or a Slack mapping is saved only while its sources are at the generations it was made under. | `TestAnImportNeedsTheReadThatResolvedItsAccount`, `TestAnImportIsSavedOnlyAtTheGenerationsItWasMadeUnder`, `TestSlackMappingsRefuseAMovedConnection` (`storage/postgres`), `TestAMappingIsNotSavedAfterSlackIsDetached` (`cmd/gtm-connector`) |
| The Salesforce and Slack readers' own transports dial nothing, and `sf` is never run, unless `sourcehttp.Live` is on, which the binary's `main` turns on. Four tests turn it on, each to run an invented `sf` of its own. The identity provider's client uses the same transport. | `TestNothingIsDialledUnlessTheProcessIsLive` (`sources/sourcehttp`), `TestAReaderWithNoOptionsReachesNothing` (`sources/salesforce`), `TestAReaderWithNoTransportReachesNothing` (`sources/slack`), `TestNothingIsReachedWithoutATransportAndNothingIsRepeated` (`oidc`) |
| `demo` builds its cards from the book inside the binary, and makes them in its tests with `sourcehttp.Live` off. | `TestDemoPublishesTheBookForTheDemoSignIn` (`cmd/gtm-connector`) |

A read across a source change reads nothing and binds nothing, and what the caller sees depends on
where the change is caught:

| Where the change is caught | The receipt | The caller | Test |
| --- | --- | --- | --- |
| A command (`accounts`, `inspect`, `brief`) whose source moved after it was opened | error/409 `source_changed` | Exit 3, in a fixed line | `TestACommandReadsNothingFromASourceChangedAfterItWasOpened`, `TestASourceChangedDuringAReadIsRefused` (`cmd/gtm-connector`) |
| An MCP call whose source moved before its receipt started | error/200 `request_failed` | HTTP 200, a tool error `source_changed` | `TestACallReadsNothingFromASourceThatMovedBeforeItsReceipt` (`mcptools`), for the refusal |
| A read that finishes after its source moved | error/409 `source_changed`, finished by the database | Inside a card, no card. Over MCP, the gate's HTTP 503 `execution_or_audit_unavailable`. | `TestSourceChangeFencesReadsAndPublication`, `TestTheDatabaseFencesAReadAcrossASourceChange` (`storage/postgres`), `TestReceiptsRuleOnPostgres` (`cards`), for the receipt and the card |
| A card published after one of its sources moved | The seal: error `source_changed` | No card | `TestSourceChangeFencesReadsAndPublication` (`storage/postgres`) |

## 7. Credentials never reach output or logs

**A credential is held where it is needed and printed nowhere. What the connector keeps of a bearer is
its SHA-256.**

| Claim | Test |
| --- | --- |
| The bearer a request presents to `/mcp` reaches no reply, header, challenge or error, and the database keeps its SHA-256 only, whether the bearer was admitted, refused, or presented while the store failed. | `TestMountedCredentialsReachNothing` (`transport/mcp`) |
| A request that repeats the bearer it presents, in its ID, tool name, arguments or brief ID, is refused in fixed words, and its receipt holds none of them. | `TestMountedARequestThatRepeatsItsCredential` (`transport/mcp`) |
| A token from `token new` is not in any receipt, in `audit`, in `token list` or in the log of `serve`, and the log holds no session key. | `TestATokenFromTheCommandOpensMCP`, `TestServeAnswersMCPOverTLS` (`cmd/gtm-connector`) |
| `serve` logs a database failure of its housekeeping by its fixed line, as a command prints one, never by the driver's own text. | `TestServeLogsAFailingStoreByItsCodeOnly` (`cmd/gtm-connector`) |
| The credentials folder is a real folder of mode 0700. Each file in it is a regular file of mode 0600 with one link and at most 64 KiB, checked on the descriptor that is read, so a link or a swapped path is refused. | `TestCredentialsFolderIsPrivate`, `TestCredentialsStayAttachedToTheVerifiedFiles`, `TestCredentialsAreReadWithinTheirSize` (`cmd/gtm-connector`) |
| A Slack sign-in is saved as `slack-WORKSPACE.json`, mode 0600, in the credentials folder, never in the database. No Slack token reaches any output, receipt or row. | `TestSlackFromSignInToACard` (`cmd/gtm-connector`), `TestNoCredentialIsPrinted` (`sources/slack`) |
| The Slack sign-in gives the system browser a one-time start page on its loopback callback, never Slack's address, the state or the PKCE challenge, and none of the connector's settings. The callback takes only a GET of its own path with its Host and state, and the code is exchanged with the verifier of an S256 challenge. | `TestTheSystemBrowserIsGivenOnlyTheStartPage`, `TestSlackFromSignInToACard` (`cmd/gtm-connector`), `TestTheCallbackTakesOnlyItsOwnRequest` (`sources/slack`) |
| The start page is answered once: a second request of it gets 404. It is taken only from a request that states no `Origin` or `Sec-Fetch-Site` of another site and carries no sign-in cookie, and a refused first request ends it too. It sets a cookie of a random value, `HttpOnly`, `SameSite=Lax`, for the callback's path, for the sign-in's ten minutes, named for the sign-in: the redirect's port and a random suffix, so a cookie of another sign-in or of another program on localhost is neither taken nor overwritten. The callback is taken only with that cookie, once, so it comes from whoever loaded the start page; a browser's cookie jar keeps it for the callback's path only. | `TestTheCallbackTakesOnlyItsOwnRequest` (`sources/slack`), `TestTheSystemBrowserIsGivenOnlyTheStartPage`, `TestSlackFromSignInToACard` (`cmd/gtm-connector`) |
| When the Slack sign-in refuses a grant for scopes other than the ones asked for, or for an approval not confirmed at the terminal, and the grant's token expires, it asks Slack to revoke that access token under a `slack_sign_in_revoke` receipt committed first, saves no new credential, and says in the refusal whether Slack confirmed; a token Slack did not confirm revoking may still be live. A token that does not expire is not revoked, since another saved sign-in may read with it, and neither is one that is the saved sign-in's own, which still passes its check. | `TestAGrantTheSignInDoesNotKeepIsRevoked` (`cmd/gtm-connector`) |
| A new Slack approval is saved only on a `yes` typed at the terminal that runs `debug slack connect`, read from `/dev/tty`, after it names the Slack user, team, workspace and app the approval came from. Any other answer, or no terminal, refuses with `slack_login_unconfirmed` and saves nothing. | `TestASignInIsSavedOnlyOnTheOperatorsYes` (`cmd/gtm-connector`) |
| No Salesforce token is saved: a workspace keeps the alias, the environment and a digest of the identity, and no token, org ID or address. A token given as `--org` never reaches `sf`, a row or the screen, and a token handed to the process reaches no output, row or receipt. | `TestSalesforceIsReadByEveryCommand`, `TestAnOrgArgumentIsAnAliasOrNothing`, `TestASalesforceTokenHandedToTheProcessIsReadByEveryCommand` (`cmd/gtm-connector`) |
| Signing in through `sf` runs exactly `sf org display` and `sf org auth show-access-token`, each with the alias and `--json`, with no input, its error output discarded, and its telemetry, updates and log files off. An alias `sf` could read as an option, or one holding a space or a control character, never reaches it. | `TestSigningInRunsTwoCommandsAndReadsNothing`, `TestExecRunsTheProgramWithItsNoiseOff`, `TestAnAliasThatIsNotOneNeverReachesSF` (`sources/salesforce`) |
| The Salesforce token file is a regular file only its owner can read, or a link to one in a folder others cannot write, and a refusal gives its mode, never its path or the token. A database password file others can read is refused before any connection, by its mode, never by what it holds. | `TestTheTokenFileMustBePrivateAndPlain` (`sources/salesforce`), `TestAPasswordFileOthersCanReadIsRefused` (`cmd/gtm-connector`) |
| A program the connector starts, `sf` or the browser opener, gets none of `GTM_*`, `PG*`, `*_TOKEN`, `*_TOKEN_FILE` or `SALESFORCE_INSTANCE_URL`. | `TestNoSecretReachesAChild` (`sources/childenv`), `TestSFIsGivenNoCredentialOfTheConnector` (`sources/salesforce`), `TestTheSystemBrowserIsGivenOnlyTheStartPage` (`cmd/gtm-connector`) |
| An answer from Salesforce or Slack that holds a token the reader has held, also one written with escapes, is refused whole, so a token never becomes a record, a cursor or a saved address. The search finds a token in any case and in composed form too. | `TestAnAnswerHoldingATokenIsRefused` (`sources/salesforce`, `sources/slack`), `TestATokenOfAnEarlierReadNeverBecomesARecord` (`sources/salesforce`), `TestTextACardWouldTurnIntoATokenIsFound` (`sources/echo`) |
| The readers speak HTTP/1.1 only, so Go's HTTP/2 debug log cannot print a bearer. They check the server's certificate, refuse TLS below 1.2, take no proxy and follow no redirect. | `TestTheBearerIsNotInTheHTTP2DebugLog`, `TestTheReadersOwnTransportSpeaksHTTP1`, `TestAnswersOutsideTheShapeAreRefused` (`sources/salesforce`), `TestTheCertificateAndTheTLSVersionAreChecked` (`sources/sourcehttp`), `TestTheReadersOwnTransport`, `TestOverAConnection` (`sources/slack`) |
| An error the connector did not word itself prints a fixed line or a SQLSTATE, never a driver's text, which can name a host, a role or a password. The install commands never print back what they were given, and a source's refusal is its fixed code. | `TestAnUnexpectedErrorPrintsNoneOfItsText`, `TestAFailingStorePrintsItsCodeOnly`, `TestTheInstallCommandsPrintNoneOfWhatTheyWereGiven` (`cmd/gtm-connector`), `TestAnAnswerSFDidNotGiveIsNoLogin` (`sources/salesforce`), `TestAFailedReadKeepsOnlyTheSourcesDiagnostic` (`cards`) |
| `debug salesforce check` prints no record, account or org ID, address or token. The report of a Slack read, which `debug slack check` prints from, holds no ID, timestamp, token or message text. | `TestSalesforceIsReadByEveryCommand` (`cmd/gtm-connector`), `TestTheReportHoldsNoSourceValue` (`sources/slack`) |
| A backup is written with mode 0600 and never over another file, and it carries the credential files `setup` makes. It leaves the Slack sign-ins out, since a copy of one could still be a valid sign-in, and the identity provider's client secret. | `TestBackupAndRestoreKeepState`, `TestABackupLeavesTheSlackSignInOut` (`cmd/gtm-connector`) |
| The identity provider's client secret is the file `oidc-client-secret` in the credentials folder, under the folder's rules, and never an environment variable. The secret and the ID token reach no refusal and no answer of the server. The log of `serve` gives a refused sign-in's reason in fixed words, with no code, state, secret or token. A refused setting is not printed back, since an issuer can name a company's tenant. | `TestTheClientSecretIsACredentialFile`, `TestServeRefusesSignInSettingsItCannotServe`, `TestGrantsThroughTheCommandOpenAccountsInTheBrowser` (`cmd/gtm-connector`), `TestSignInsThatAreRefused` (`transport/http`), `TestNothingIsReachedWithoutATransportAndNothingIsRepeated` (`oidc`) |
| `setup` writes no owner secret, which no command uses. It leaves an `owner-secret` file an earlier setup wrote as it is and says so in one line, a backup does not carry it, and a backup of an earlier setup that carries it restores without installing it. | `TestNoCommandKeepsTheUnusedOwnerSecret` (`cmd/gtm-connector`) |

## Claims no test checks yet

- A grant revoked while a refresh reads ends it with 403 and no card. The maker reads the grant again
  before each read and as it publishes; no test revokes one during a refresh.
- A refresh maps a failed read to 502 and evidence that fails a check to 422. The route test shows the
  mapping from the registry's errors, not a source that fails.

- `serve` finishes interrupted receipts every 30 seconds after its sweep at start. The interval is a
  fixed ticker that no test waits for.
- A receipt's lease lasts five minutes. The tests shorten it.
- With `--selection`, `serve` sends every bearer to the demo workspace's gate, which refuses a token of
  another workspace as it refuses any token it does not know. The gate's refusal is tested; the
  routing with a selection is not, because the routing test starts `serve` without one.
- Over MCP, a read that finishes after its source moved is answered with the gate's HTTP 503. The
  database's side of it is tested, the gate's answer is not.
- The TCP keepalives end a connection whose peer has gone. The test checks the settings each pooled
  connection carries, the client socket's own probe delay, interval and count, and that the server ends
  a transaction left idle, not a dead peer found by its probes. It runs against a database without TLS.
- `compose.yaml` publishes the server's and PostgreSQL's ports on the host's loopback only. No test
  reads the Compose file.
- `read_deal_card` renders a card that holds a unit sealed under older neutralizer rules again, with
  the functions `inspect` and the brief tools use. Those functions are tested; no test publishes such a
  card and reads it through the deal tool.
- A deal's and a match's account name is neutralized in `list_deals`, `read_deal_card` and the deal
  and match routes. The code neutralizes it in each; no test sends a hostile name through them.
- `read_deal_card` refuses a principal whose grant is revoked between its read of the deal and its read
  of the card. No test revokes a grant there.
- The receipt of `read_deal_card` names the deal's account for a known deal it refuses, as it does for
  one it releases. The test reads the receipt of a released card and of an unknown deal only.
- A decision of a match checks the grant again inside the transaction that commits it. The test
  revokes the grant before the decision is sent, not while it commits. A decision refused for a deal
  of another account or for a superseded match leaves a receipt finished as an error; the test reads
  those refusals' answers, not their receipts.
- The output of `list_deals` carries `untrusted_data: true`, and `read_deal_card` gives a card's lines
  and gaps as saved. The test checks the flag on the card reads and counts the lines and gaps.
- `GET /v1/matches?all=1` gives every current decision of the granted accounts. The test reads the
  waiting queue.
- No test scans every value the reporting views return for a credential. Tests check values in
  places. For example, `TestTheReceiptsViewShowsNoCallersText` shows a caller's tool name and brief
  ID as `<invalid>` and null, and `TestReportingViewsCountTheStoredRows` counts each view's rows
  against the stored rows, follows their links, and compares the views before and after a restore.

## Known limits

- **Nothing limits how often a person refreshes a deal.** Each refresh reads the deal's sources and
  publishes a card, as the person, for an account they hold a grant on. A person, or a page running in
  their session, can make the connector send as many reads to the CRM and Slack as it sends requests.

- **The owner can rewrite the evidence.** Whoever holds the owner role, or a superuser of the
  database, can switch the triggers off and change receipts and cards together. Records here are
  neither tamper-proof nor billing data, and `inspect` catches a changed card, not a determined owner.
- **A copy of the credentials folder is the install's credentials.** Anyone who can read it, or a
  backup, holds the TLS key, the session key and the demo sign-in, and the folder also holds every
  Slack sign-in. A backup also holds every record every source returned.
- **Processes of the operator are not isolated.** Anything running as the operator can read the
  credentials folder and the app role's URL, and so every connected workspace.
- **The Slack start page can be taken first.** A process that reads the browser opener's arguments can
  ask for the one-time start page before the browser does. The operator's browser then gets a 404, and
  that process holds the sign-in's cookie and can finish the sign-in with an approval of its own. Neither
  the cookie nor the single answer stops this. What stops it being saved is the question at the
  terminal: the operator is shown the Slack user, team and workspace of the approval and must type
  `yes`. An operator who says yes without reading it saves that approval. In a workspace whose Slack was
  connected before, the pin also refuses another user.
- **Every client token of a workspace reads the same accounts.** Grants belong to the workspace's one
  `MCP clients` principal, and issuing a token grants it every account of the demo book, or every
  account a command has recorded in a workspace of real sources. There is no limit for one token
  alone: a token limited to a selection is refused. Accounts narrow only when a grant of the principal
  is revoked, for all its tokens, or when a server runs with a selection, for all its clients.
- **A reporting query has no grant and no receipt.** The reporting role reads every workspace of its
  database, and PostgreSQL answers it, so the audit log holds nothing of what it read. Whoever is
  granted the role reads as the operator does, within the views.
- **The direct MCP reads return records unneutralized.** `search_accounts`, `get_account`,
  `list_opportunities`, `read_call_notes` and `read_slack_messages` return the source adapters'
  records, with their text not neutralized. The result carries `untrusted_data: true` in its
  envelope, which is the `structuredContent` and the text block that repeats it. The flag is advisory:
  the MCP client decides what it renders, keeps or sends to a model.
- **Redaction is narrow.** `/mcp` looks for the credential a request presents in that request's ID,
  tool name, arguments and brief ID. It looks for no other credential: other arguments are stored as
  given, so a different token typed into one is kept in the receipt. A source record is bound as its
  source returned it, and its reader looks in it only for that reader's own tokens.
- **A call is authorized when it starts.** A grant or a token revoked while a direct read runs does not
  stop that one answer. A prepared brief checks the bearer for the last time before it publishes, so a
  token revoked during the publication can still get its brief. `read_renewal_brief` checks the grant
  and the bearer again just before it releases.
- **The rate limit is counted in each process's memory.** `serve` and each `mcp stdio` process count
  apart, and a restart starts the count again.
- **The session cookie is bound to its host, not its port.** A browser also sends it to another
  program that serves the same host.
- **A person the identity provider disables keeps the session they hold until it expires.** That is
  60 minutes unless `GTM_OIDC_SESSION_MINUTES` says otherwise, or until the operator runs
  `grant --disable`, which ends it at once. The connector does not ask the provider again during a
  session, and does not end the provider's own session: sign-out only offers its page.
- **The identity provider says who a person is.** Its administrators can sign in as any of its people in
  an allowed domain or tenant. Grants decide what that person reads.
- **Evidence is kept for good.** The connector never deletes a receipt, a card or a record version,
  and there is no retention period. Only the operator, by removing the database, removes them.
- **Roles and quotations are as good as their labels.** A seller is whoever the operator lists. A
  speaker label is taken as the source gives it, and anyone who can start a line of a transcript
  imported with `--speaker-names` can write a contact's name there. Sentences are split at
  punctuation, which can join or divide them wrongly. A card relates no record to another, so it flags
  no contradiction, and a number attributes a source, not its truth.
- **POSIX only.** The credential checks use POSIX file modes and owners, on macOS and Linux.
- **The link to PostgreSQL is what its URL says.** `compose.yaml` connects without TLS, on Compose's own
  network.

## Open findings

None. Each finding of the earlier review is a claim above, with its test.

## Not verified

No automated test contacts a live source. The tests that read Salesforce or Slack run against an
invented org, an invented `sf` and an invented Slack, and the tests that need a database use a local
PostgreSQL. By hand, outside the tests and with no record of it in this repository, one development
Salesforce org was read, read-only, and one development Slack workspace was signed in to and read,
both on invented records, on 2026-11-21.

- Sandbox and production Salesforce behaviour has only invented answers behind it.
- No automated test signs in to a live Slack workspace. The sign-in has run by hand against one
  development workspace and one app.
- No real identity provider has been signed in to. Every sign-in runs against an invented provider, so
  the claims a real provider sends, such as `email_verified`, `hd` and `tid`, have only invented values
  behind them.
- No call provider is reached: transcripts are files the operator imports.
- The only MCP client run against the binary is the Inspector CLI, in CI. No assistant app has been
  tested.
- Cards were tried on invented deals only. Nothing here shows that a card is useful to a seller.
