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
`GTM_TEST_DATABASE_URL`, and `GTM_REQUIRE_DATABASE=1` makes it fail instead. Each test is named with its
folder: `cmd/gtm-connector`, or a folder under `internal/`.

## Who holds authority

The connector trusts the machine it runs on and the person who runs it. Everyone else reaches it
through a check: an assistant with a bearer token, and a browser with a signed-in session.

```text
assistant ─────── HTTPS, bearer ───▶ ┌──────────────────────┐ ──▶ PostgreSQL, as the app role
browser ───────── HTTPS, cookie ───▶ │ serve: /mcp and /api │
assistant app ─── stdin, stdout ───▶ │ mcp stdio            │ ──▶ Salesforce: an sf login or a token
operator ──────── database URL ────▶ │ every other command  │ ──▶ Slack: the saved sign-in
                                     └──────────────────────┘
```

Three things carry the operator's authority, and whoever holds one holds that part of it:

- **The database URL.** With the app role's URL in `GTM_DATABASE_URL`, the binary issues a token for any
  connected workspace, with any read scope, and serves `mcp stdio` for one. The owner role's URL
  (`GTM_OWNER_DATABASE_URL`) runs setup, migrations, backup and restore. That role owns the tables, so
  it can switch their triggers off.
- **The credentials folder** (`GTM_CREDENTIALS_DIR`). It holds the TLS key and certificate, the key that
  signs the browser's pre-sessions, the demo sign-in's password, an owner secret, and each workspace's
  Slack sign-in.
- **The source logins.** An `sf` alias's login or a Salesforce token handed to the process, and the
  saved Slack sign-in. The connector reads with whatever they may read, through its fixed queries.

> **The database URL is the authority.** Neither `token new` nor `mcp stdio` asks for a session or any
> other sign-in. Give the app role's URL, and its password, only to people who may read every
> connected workspace.

| Claim | Test |
| --- | --- |
| `token new` and `mcp stdio` need nothing but the app role's URL. | `TestATokenFromTheCommandOpensMCP`, `TestStdioCommandCRMOnlyReadsCRMAndNeverWidens` (`cmd/gtm-connector`) |
| The app role reads and writes rows and nothing else: it cannot disable a trigger, truncate, delete an account, drop or create a table, or change the record of migrations. `serve`, `demo` and every command that reads or connects a workspace open the database through one function, which refuses a role that owns the tables or is a superuser. | `TestTheAppRoleOnlyTouchesRows` (`storage/postgres`), `TestDemoRunsOnlyAsTheAppRole` (`cmd/gtm-connector`) |
| `serve` listens on a loopback address and refuses any other `GTM_LISTEN`. Only `GTM_CONTAINER_LISTEN`, set for a container's own network, takes another address. | `TestServeListensOnLoopbackOnly` (`cmd/gtm-connector`) |
| `serve` speaks TLS 1.3 only, with the certificate `setup` wrote. | `TestServeSpeaksOnlyTLS13`, `TestServeAnswersMCPOverTLS` (`cmd/gtm-connector`) |
| Outside `/mcp`, `serve` answers only the Hosts it is configured with, which also blocks DNS rebinding: any other Host gets 421, and another allowed Host is sent on to the public origin. `/mcp` admits the public origin's own Host alone (rule 3). | `TestWrongHostIsRefused`, `TestOtherAllowedHostsGoToThePublicOrigin` (`transport/http`), `TestOptionsStarMeetsTheHostCheck` (`cmd/gtm-connector`) |
| A browser signs in only with a password, and the database holds a password sign-in only for a person of a demo workspace. A principal reads nothing of another workspace, so no browser session reads a workspace of real sources. | `TestSessionsRotateExpireAndRevoke` (`storage/postgres`), `TestOtherWorkspacesAndMissingThings` (`authz`) |

### Tokens

`token new` makes a bearer token for one workspace, prints it once, and stores only its SHA-256. Every
client token of a workspace belongs to one principal, `MCP clients`, and reads the accounts that
principal is granted. `mcp stdio` makes a token of its own for its process. [MCP](docs/mcp.md#tokens)
describes both.

> **Issuing a token widens every client token of its workspace.** Issuing one, or starting
> `mcp stdio`, grants the `MCP clients` principal every account of the connected CRM it was never
> granted. A grant revoked for the principal stays revoked.

| Claim | Test |
| --- | --- |
| A token is printed once and stored as its SHA-256. It carries only the scopes given, from `crm:read`, `calls:read` and `slack:read`, for five minutes, or 1 to 60 with `--minutes`. `--scope crm:write` is refused before anything is issued. | `TestATokenFromTheCommandOpensMCP`, `TestTokenArguments`, `TestClientTokenGrantsAndWorkspaces` (`cmd/gtm-connector`) |
| Issuance grants the principal every account of the CRM and keeps a revoked grant revoked. It is one transaction, so a failed issuance grants nothing. A disabled principal gets no token and no stdio server. | `TestATokenFromTheCommandOpensMCP`, `TestClientTokenGrantsAndWorkspaces`, `TestAFailedIssuanceWidensNothing` (`cmd/gtm-connector`) |
| `serve` answers a token in the workspace it was issued in, with that workspace's sources, and receipts its calls there. | `TestClientTokenGrantsAndWorkspaces` (`cmd/gtm-connector`) |
| A revoked token is refused from its next call. | `TestATokenFromTheCommandOpensMCP`, `TestStdioCommandGrantsAndRevocation` (`cmd/gtm-connector`) |
| A token gets 60 tool calls in 60 seconds, and every call it makes counts, refused or not. The next one is refused 429 `rate_limited` with `Retry-After`. | `TestRateLimitCountsScopeDenials` (`transport/mcp`), `TestStdioCommandChargesScopeDenials` (`cmd/gtm-connector`) |
| The token of `mcp stdio` lasts at most two minutes unless the running process renews it. It is revoked when the session ends: at the end of input, when the app closes stdout, and when the app stops reading. | `TestStdioTokenLastsTwoMinutes`, `TestStdioCommandCRMOnlyReadsCRMAndNeverWidens`, `TestStdioClosedStdoutRevokesTheToken`, `TestStdioClientThatStopsReadingRevokesTheToken` (`cmd/gtm-connector`) |

## 1. A receipt comes before every tool call

**Every tool call leaves a receipt in PostgreSQL, committed before the call is checked or run, and
finished before anything is released.**

```text
tools/call ──▶ receipt committed (pending) ──▶ checks ──▶ the tool reads ──▶ receipt finished ──▶ answer
                                                  └──▶ refused ──▶ receipt finished as an error ──▶ refusal
```

The gate commits the receipt first, so a call refused for its token, its scope, its arguments or its
Host is receipted too. A request that calls no tool, such as `tools/list`, gets none. The database
holds each receipt to its rules with triggers, so a writer that goes around the connector's code meets
the same rules. [Audit](docs/audit.md) describes what a receipt holds.

> **No receipt, no call.** When the receipt cannot be committed, nothing runs and the answer is HTTP
> 503 `audit_unavailable`. When it cannot be finished, nothing is released.

| Claim | Test |
| --- | --- |
| Every tool call, over HTTPS and over stdio, gets one receipt. | `TestMountedCallsLeaveOneOuterReceipt` (`transport/mcp`), `TestStdioCommandCRMOnlyReadsCRMAndNeverWidens` (`cmd/gtm-connector`) |
| Every source read starts after its pending receipt is committed, as a second connection to the database sees it, over HTTPS and over stdio. | `TestMountedCallsLeaveOneOuterReceipt`, `TestMountedStuckReceiptsOverStdio` (`transport/mcp`) |
| A refused call keeps its receipt, finished as an error with no rows, and reads nothing: a bad token, a missing scope, the write tool, invalid arguments, an unknown tool, an account outside the grant or the selection, the wrong Host, an `Origin` header, a body that cannot be parsed or arrives cut off, arguments that hold a NUL, a request that repeats its own credential, and a stdio line past 4 MiB. | `TestMountedCallsLeaveOneOuterReceipt`, `TestMountedBadTokensAreRefused`, `TestMountedGrantsLimitEveryTool`, `TestMountedHostAndOriginChecks`, `TestMountedEveryRefusedCallLeavesItsReceipt`, `TestMountedARequestThatRepeatsItsCredential`, `TestStdioOversizedCallKeepsAReceipt` (`transport/mcp`), `TestStdioCommandMalformedCallsStillLeaveReceipts` (`cmd/gtm-connector`) |
| A request that calls no tool gets no receipt. | `TestMountedCallsLeaveOneOuterReceipt` (`transport/mcp`) |
| With the receipt store down at the start, nothing runs and the answer is 503 `audit_unavailable`. Down at the finish, nothing is released, not even a refusal's own words, and the receipt stays pending. | `TestMountedCallsLeaveOneOuterReceipt` (`transport/mcp`) |
| A receipt's start or finish waits on a lock no longer than a fixed limit. A stuck database refuses the call, or withholds its answer, at the gate's own deadline. | `TestMountedStuckReceiptsOverHTTPS`, `TestMountedStuckReceiptsOverStdio` (`transport/mcp`), `TestAReceiptStartOrFinishGivesUpOnALock` (`storage/postgres`) |
| A caller who leaves is answered nothing, nothing is read after it left, and its receipt is finished as an error. A brief whose publication has begun completes and is sealed. | `TestMountedACallerWhoLeaves` (`transport/mcp`) |
| A receipt left pending by a process that stopped is finished as error/503 `interrupted` once its lease expires. `setup`, `mcp stdio` and `serve` do this as they start. | `TestExpiredLeaseCannotFinish` (`storage/postgres`), `TestStdioAndSetupFinishInterruptedReceipts`, `TestServeFinishesInterruptedReceiptsAsItStarts` (`cmd/gtm-connector`) |
| For any writer, a receipt finishes once, and only by the holder of its live lease. A parent finishes after its children, an error has no rows, and a receipt's arguments never change. Receipts are never deleted or truncated. | `TestReceiptFinishesOnce`, `TestExpiredLeaseCannotFinish`, `TestTheLeaseHolderIsEnforcedByTheDatabase`, `TestParentsAndChildren`, `TestArgumentsKeepTheirOrder` (`storage/postgres`) |
| The commands read under receipts too: a card's reads, and `accounts` and `inspect` on a real CRM. With no receipt store, nothing is read, and an owner read is never left pending. | `TestReceiptsRuleOnPostgres` (`cards`), `TestSalesforceIsReadByEveryCommand`, `TestAnOwnerReadIsNeverLeftPending` (`cmd/gtm-connector`) |

## 2. A scope opens only its own source

**A token reads a source only with that source's scope, and an account only through its principal's
live grant. A selection narrows the accounts further.**

The gate checks the scope before the tool runs, and the tool's handler checks it again. The tool then
checks the selection, when there is one, and the grant, before it reads anything. A search or a list
asks the source only for accounts that pass both. [MCP](docs/mcp.md#tools-and-scopes) lists each tool
and its scope.

| Claim | Test |
| --- | --- |
| A token reads only the source its scope opens. Every other tool answers 403 `insufficient_scope`, naming the scope it needs, and its source is not read. | `TestMountedScopeOpensOnlyItsSource` (`transport/mcp`), `TestStdioCommandCRMOnlyReadsCRMAndNeverWidens` (`cmd/gtm-connector`) |
| The tool's handler refuses a call whose admitted scopes do not cover its tool, before the tool runs. | `TestHandlerRefusesAnUncoveredScope` (`transport/mcp`) |
| A token with any scope outside the three read scopes opens nothing. It is refused whole, never trimmed, and the database stores no other scope. | `TestATokenWithAScopeOutsideTheReadSetOpensNothing` (`transport/mcp`), `TestTheReadScopesMigrationRefusesAWriteScope` (`storage/postgres`) |
| No tool writes. `tools/list` names no write tool, and a call of `log_activity` is refused 403 `crm_write_disabled` under its receipt. | `TestToolsListHasNoWriteTool`, `TestWriteWithAReadGrantIsRefused`, `TestMountedCallsLeaveOneOuterReceipt` (`transport/mcp`) |
| An account outside the principal's live grants is refused before any read, and left out of searches and lists. A revoked grant stops a saved brief from being released. | `TestMountedGrantsLimitEveryTool` (`transport/mcp`), `TestStdioCommandGrantsAndRevocation` (`cmd/gtm-connector`) |
| A Salesforce search puts its text, as literal text, into a fixed query, and the accounts the caller may read into the query itself. | `TestSearchesAreFixedQueries` (`sources/salesforce`) |
| A Slack read asks for messages only with a GET of `conversations.replies` for the account's mapped thread. | `TestOneRequestAndTheMessagesAsGiven` (`sources/slack`) |
| With a selection, an unselected account is refused before any read, Slack is read only where the selection includes it, searches and lists hold selected accounts only, and the two brief tools are served. Without one, they are not. | `TestMountedSelectionLimitsAccounts` (`transport/mcp`), `TestStdioCommandWithASelection` (`cmd/gtm-connector`) |
| A selection file is a regular file, not a symbolic link, that no one but its owner may write, at most 65,536 bytes, in one shape. An empty `--selection` is refused, never read as no selection. | `TestLoadSelection`, `TestServeSelectionFlag`, `TestMCPArgumentsNeedNoDatabase` (`cmd/gtm-connector`) |
| A prepared brief checks the bearer again before each read inside it, counts each read against the bearer's rate, and goes by the scopes the bearer carries at that moment. A bearer that expires part way leaves no brief. | `TestMountedReadsInsideABriefCheckTheBearerAgain`, `TestMountedABriefGoesByTheScopesTheBearerCarriesNow` (`transport/mcp`) |
| A card is published only if its principal still holds a grant on the card's own account, checked in the publication's transaction. | `TestPublicationRechecksTheGrant`, `TestPublicationRechecksTheCardsOwnGrant` (`storage/postgres`) |

In the browser, a session reads a card, a unit or a receipt only through a grant on the card's account.

| Claim | Test |
| --- | --- |
| Every evidence read needs a live session and a grant. A grant on one account opens no other, a revoked grant reads nothing, and an ID that does not exist is refused like one not granted. | `TestEvidenceReadsNeedAGrant`, `TestAGrantOnOneAccountOpensNoOtherOverHTTP` (`transport/http`), `TestGrantsGuardEveryUnitAndReceiptRead` (`authz`), `TestRefusalsAreUniform` (`storage/postgres`) |
| No answer under the API, MCP, discovery or health paths may be kept by a browser or a proxy: not the session, a card, its bytes, a unit or a receipt. | `TestNoAPIResponseIsKept`, `TestNoSignedInResponseIsKept` (`transport/http`) |

## 3. A bad or expired token is refused, never retried

**A token that does not verify gets one refusal in fixed words, and nothing is tried in its place.**

| Claim | Test |
| --- | --- |
| A bearer that is missing, malformed, unknown, expired, revoked, of a disabled principal or limited to a selection, two `Authorization` headers, and a session cookie in a bearer's place are each refused 401 `invalid_token` in the same words. Each refused call leaves one receipt, reads nothing and charges no rate. | `TestMountedBadTokensAreRefused` (`transport/mcp`) |
| The database's clock decides expiry: a process whose clock is behind still refuses a token the database says has expired. | `TestMountedExpiryIsTheDatabasesClock` (`transport/mcp`) |
| Once the token of `mcp stdio` has expired, calls and listings are refused, nothing is retried with another token, and the expired token is never renewed. | `TestStdioCommandExpiredTokenIsRefusedNotRetried` (`cmd/gtm-connector`) |
| The browser API takes its session cookie only. A bearer, alone or beside the cookie, is refused, and the cookie opens nothing at `/mcp`. | `TestCookiesAndBearerTokensNeverCross` (`transport/http`), `TestMountedBadTokensAreRefused` (`transport/mcp`) |
| The session cookie is `__Host-` named, `Secure`, `HttpOnly` and `SameSite=Lax`. Sign-in rotates the session and its CSRF token, a pre-session signs in once, and a session expires, can be revoked, and ends when its principal is disabled. | `TestSignInRotatesTheSessionAndCSRF`, `TestConcurrentSignInsFromOnePreSession`, `TestSessionsExpireAndCanBeRevoked`, `TestADisabledPrincipalIsSignedOut` (`transport/http`) |
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
| The card maker writes a card from its evidence alone: from the recorded evidence it rebuilds all 450 recorded cards byte for byte, with the same units, gaps and seal. | `TestCardsFromDemoStores`, `TestCardsFromRecordedInputs` (`cards`) |
| The database refuses a card whose seal does not name its bytes, whose unit and gap rows differ from its bytes, or whose units cite a read that did not finish ok, a read of another account or brief, a read whose source has moved, or a record version that read did not bind. | `TestCardSealMustNameItsBytes`, `TestTheTriggersHoldTheCardRules`, `TestACardHoldsExactlyTheRowsItCounts`, `TestPublishChecksEveryUnitsProvenance` (`storage/postgres`) |
| A card, its units, gaps and lines, and every record version and binding are never changed or deleted once written. | `TestDurableRowsRefuseChange` (`storage/postgres`) |
| Evidence that fails a check gives no card: a refused evidence vector, a live thread that is not its snapshot, another workspace's transcript, and a read whose source moved. | `TestEvidenceVectors` (`evidence`), `TestAThreadThatFailsItsSnapshotGivesNoCard`, `TestAnotherWorkspacesTranscriptGivesNoCardOnPostgres`, `TestReceiptsRuleOnPostgres` (`cards`) |
| The verifier `inspect` runs refuses a changed byte, a value written another way, a seal that does not name the bytes, and a read receipt changed in any column. | `TestVerifyRefuses`, `TestVerifyComparesEveryColumnOfEveryRead` (`cards`) |
| A restore refuses any row the write path would refuse, also when the archive's digests were computed again. | `TestRestoreRefusesEditedArchives` (`cmd/gtm-connector`), `TestRestoreRefusesRowsTheWritePathRefuses`, `TestRestoreRefusesLinesThePublicationRefuses` (`storage/postgres`) |

## 5. Source text is data

**Text from a source is neutralized before the connector displays it, and no tool writes, so nothing a
record says can make the connector write.**

Neutralizing turns Slack mentions, links, control and format characters, and bracketed numbers into
inert words. A link shows the host a browser would open, or `[link unknown host]`. A quotation is one
whole sentence with its speaker's role, which comes from a label and never from wording.

| Claim | Test |
| --- | --- |
| Source text is neutralized as a recorded corpus of it was: mentions, links in every form, control characters and bracketed numbers become inert words. A host a browser would rewrite or refuse is not shown. | `TestSafeTextCorpus`, `TestLinkHost`, `TestAmbiguousLinkHost`, `TestMarkdownLinkWithAnyScheme`, `TestHostABrowserRewritesOrRefuses` (`evidence`) |
| The browser's account names, the receipt arguments `inspect` shows and the names of members in printed JSON are neutralized too, and `--json` writes format characters as escapes. | `TestAccountsListThePrincipalsGrants` (`transport/http`), `TestInspectShowsAReceiptsArgumentsInert`, `TestNeutralMakesMemberNamesInert`, `TestFormatCharactersAreEscapedInJSON` (`cmd/gtm-connector`) |
| A quotation is a whole sentence: one an excerpt's edge cuts is counted and not shown, and a figure inside a sentence stays there and never becomes a record value. | `TestCardsFromDemoStores` (`cards`, its ten invented cases), `TestExcerptEdgeCutsASentence` (`evidence`) |
| A speaker's role comes from a label, never from wording: a seller's retelling stays the seller's, a name a seller and a contact share is unknown, and a stated name is the only label of its cue. | `TestCardsFromDemoStores` (`cards`, its ten invented cases), `TestAStatedNameIsTheOnlyLabelOfACue` (`evidence`), `TestAVoiceTagAfterAStatedNameIsRefused` (`sources/calls`) |
| Every response of the web app carries a Content-Security-Policy that names no other origin and allows no inline script but the theme script, which it names by its hash. | `TestEveryDocumentIsUnderThePolicy` (`transport/http`) |

The five direct MCP reads return records as their sources gave them. [Known limits](#known-limits)
says what that leaves to the client.

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
| A read whose source moved before or during it is finished error/409 `source_changed` and binds nothing, and the database refuses a read, or a card, that would finish across the change. | `TestSourceChangeFencesReadsAndPublication`, `TestTheDatabaseFencesAReadAcrossASourceChange` (`storage/postgres`), `TestACallReadsNothingFromASourceThatMovedBeforeItsReceipt` (`mcptools`), `TestACommandReadsNothingFromASourceChangedAfterItWasOpened` (`cmd/gtm-connector`) |
| `serve` and `mcp stdio` read the sources as connected when each call starts: a source removed while they run is refused, and read again once connected again. | `TestServeFollowsWhatTheWorkspaceIsConnectedTo`, `TestStdioReadsTheSourcesAsTheyAreConnectedNow` (`cmd/gtm-connector`) |
| A source stays pinned to the identity it was first connected to, also after it is removed. Another org or Slack user is refused, and the database refuses a changed pin from any writer. | `TestUpdateSourcesSavesAChangeAndFencesIt` (`storage/postgres`), `TestAWorkspaceKeepsTheCRMItWasPinnedTo`, `TestAPinnedWorkspaceRefusesASignInOfAnotherUser` (`cmd/gtm-connector`) |
| A transcript import is saved only under the workspace's own finished read that returned its account. An import or a Slack mapping is saved only while its sources are at the generations it was made under. | `TestAnImportNeedsTheReadThatResolvedItsAccount`, `TestAnImportIsSavedOnlyAtTheGenerationsItWasMadeUnder`, `TestSlackMappingsRefuseAMovedConnection` (`storage/postgres`), `TestAMappingIsNotSavedAfterSlackIsDetached` (`cmd/gtm-connector`) |
| The Salesforce and Slack readers' own transports dial nothing, and `sf` is never run, unless `sourcehttp.Live` is on, which the binary's `main` turns on. Four tests turn it on, each to run an invented `sf` of its own. | `TestNothingIsDialledUnlessTheProcessIsLive` (`sources/sourcehttp`), `TestAReaderWithNoOptionsReachesNothing` (`sources/salesforce`), `TestAReaderWithNoTransportReachesNothing` (`sources/slack`) |
| `demo` builds its cards from the book inside the binary, and makes them in its tests with `sourcehttp.Live` off. | `TestDemoPublishesTheBookForTheDemoSignIn` (`cmd/gtm-connector`) |

## 7. Credentials never reach output or logs

**A credential is held where it is needed and printed nowhere. What the connector keeps of a bearer is
its SHA-256.**

| Claim | Test |
| --- | --- |
| No bearer reaches anything `/mcp` answers or the database keeps: a reply, header, challenge or error, a receipt, a saved brief or a record. This holds for a bearer admitted, refused, or presented while the store failed. | `TestMountedCredentialsReachNothing` (`transport/mcp`) |
| A request that repeats its own bearer in its ID, tool name, arguments or brief ID is refused in fixed words, and its receipt holds none of them. | `TestMountedARequestThatRepeatsItsCredential` (`transport/mcp`) |
| A token from `token new` is not in any receipt, in `audit`, in `token list` or in the log of `serve`, and the log holds no session key. | `TestATokenFromTheCommandOpensMCP`, `TestServeAnswersMCPOverTLS` (`cmd/gtm-connector`) |
| The credentials folder is a real folder of mode 0700. Each file in it is a regular file of mode 0600 with one link and at most 64 KiB, checked on the descriptor that is read, so a link or a swapped path is refused. | `TestCredentialsFolderIsPrivate`, `TestCredentialsStayAttachedToTheVerifiedFiles`, `TestCredentialsAreReadWithinTheirSize` (`cmd/gtm-connector`) |
| A Slack sign-in is saved as `slack-WORKSPACE.json`, mode 0600, in the credentials folder, never in the database. No Slack token reaches any output, receipt or row. | `TestSlackFromSignInToACard` (`cmd/gtm-connector`), `TestNoCredentialIsPrinted` (`sources/slack`) |
| The Slack sign-in gives the system browser a one-time start page on its loopback callback, never Slack's address, the state or the PKCE challenge, and none of the connector's settings. The callback takes only a GET of its own path with its Host and state, and the code is exchanged with the verifier of an S256 challenge. | `TestTheSystemBrowserIsGivenOnlyTheStartPage`, `TestSlackFromSignInToACard` (`cmd/gtm-connector`), `TestTheCallbackTakesOnlyItsOwnRequest` (`sources/slack`) |
| No Salesforce token is saved: a workspace keeps the alias, the environment and a digest of the identity, and no token, org ID or address. A token given as `--org` never reaches `sf`, a row or the screen, and a token handed to the process reaches no output, row or receipt. | `TestSalesforceIsReadByEveryCommand`, `TestAnOrgArgumentIsAnAliasOrNothing`, `TestASalesforceTokenHandedToTheProcessIsReadByEveryCommand` (`cmd/gtm-connector`) |
| Signing in through `sf` runs exactly `sf org display` and `sf org auth show-access-token`, each with the alias and `--json`, with no input, its error output discarded, and its telemetry, updates and log files off. An alias `sf` could read as an option, or one holding a space or a control character, never reaches it. | `TestSigningInRunsTwoCommandsAndReadsNothing`, `TestExecRunsTheProgramWithItsNoiseOff`, `TestAnAliasThatIsNotOneNeverReachesSF` (`sources/salesforce`) |
| The Salesforce token file is a regular file only its owner can read, or a link to one in a folder others cannot write, and a refusal gives its mode, never its path or the token. A database password file others can read is refused before any connection, by its mode, never by what it holds. | `TestTheTokenFileMustBePrivateAndPlain` (`sources/salesforce`), `TestAPasswordFileOthersCanReadIsRefused` (`cmd/gtm-connector`) |
| A program the connector starts, `sf` or the browser opener, gets none of `GTM_*`, `PG*`, `*_TOKEN`, `*_TOKEN_FILE` or `SALESFORCE_INSTANCE_URL`. | `TestNoSecretReachesAChild` (`sources/childenv`), `TestSFIsGivenNoCredentialOfTheConnector` (`sources/salesforce`), `TestTheSystemBrowserIsGivenOnlyTheStartPage` (`cmd/gtm-connector`) |
| An answer from Salesforce or Slack that holds a token the reader has held, also one written with escapes, is refused whole, so a token never becomes a record, a cursor or a saved address. The search finds a token in any case and in composed form too. | `TestAnAnswerHoldingATokenIsRefused` (`sources/salesforce`, `sources/slack`), `TestATokenOfAnEarlierReadNeverBecomesARecord` (`sources/salesforce`), `TestTextACardWouldTurnIntoATokenIsFound` (`sources/echo`) |
| The readers speak HTTP/1.1 only, so Go's HTTP/2 debug log cannot print a bearer. They check the server's certificate, refuse TLS below 1.2, take no proxy and follow no redirect. | `TestTheBearerIsNotInTheHTTP2DebugLog`, `TestTheReadersOwnTransportSpeaksHTTP1`, `TestAnswersOutsideTheShapeAreRefused` (`sources/salesforce`), `TestTheCertificateAndTheTLSVersionAreChecked` (`sources/sourcehttp`), `TestTheReadersOwnTransport`, `TestOverAConnection` (`sources/slack`) |
| An error the connector did not word itself prints a fixed line or a SQLSTATE, never a driver's text, which can name a host, a role or a password. The install commands never print back what they were given, and a source's refusal is its fixed code. | `TestAnUnexpectedErrorPrintsNoneOfItsText`, `TestAFailingStorePrintsItsCodeOnly`, `TestTheInstallCommandsPrintNoneOfWhatTheyWereGiven` (`cmd/gtm-connector`), `TestAnAnswerSFDidNotGiveIsNoLogin` (`sources/salesforce`), `TestAFailedReadKeepsOnlyTheSourcesDiagnostic` (`cards`) |
| `debug salesforce check` prints no record, account or org ID, address or token. The report of a Slack read, which `debug slack check` prints from, holds no ID, timestamp, token or message text. | `TestSalesforceIsReadByEveryCommand` (`cmd/gtm-connector`), `TestTheReportHoldsNoSourceValue` (`sources/slack`) |
| A backup is written with mode 0600 and never over another file, and it carries the credential files `setup` makes. It leaves the Slack sign-ins out. | `TestBackupAndRestoreKeepState`, `TestABackupLeavesTheSlackSignInOut` (`cmd/gtm-connector`) |

## Claims no test checks yet

- `serve` finishes interrupted receipts every 30 seconds after its sweep at start. The interval is a
  fixed ticker that no test waits for.
- A receipt's lease lasts five minutes. The tests shorten it.
- MCP results mark their records `untrusted_data: true`. Only the Inspector job checks it on the
  binary's own tools, and it needs Node and a download, so it was not run for this review.
- `compose.yaml` publishes the server's and PostgreSQL's ports on the host's loopback only. No test
  reads the Compose file.

## Known limits

- **The owner can rewrite the evidence.** Whoever holds the owner role, or a superuser of the
  database, can switch the triggers off and change receipts and cards together. Records here are
  neither tamper-proof nor billing data, and `inspect` catches a changed card, not a determined owner.
- **A copy of the credentials folder is the install's credentials.** Anyone who can read it, or a
  backup, holds the TLS key, the session key and the demo sign-in, and the folder also holds every
  Slack sign-in. A backup also holds every record every source returned.
- **Processes of the operator are not isolated.** Anything running as the operator can read the
  credentials folder and the app role's URL, and so every connected workspace.
- **A token reads its workspace's whole CRM.** Grants belong to the workspace's one `MCP clients`
  principal, and issuing a token grants it every account of the CRM. There is no limit for one token
  alone: a token limited to a selection is refused. Accounts narrow only when a grant of the principal
  is revoked, for all its tokens, or when a server runs with a selection, for all its clients.
- **The direct MCP reads return records unneutralized.** `search_accounts`, `get_account`,
  `list_opportunities`, `read_call_notes` and `read_slack_messages` return each record as its source
  gave it. Neutralizing applies to what the connector displays: cards, units, the CLI and the web app.
  An assistant that shows a record's text shows it as the source wrote it.
- **A call is authorized when it starts.** A grant or a token revoked while a direct read runs does not
  stop that one answer. The two brief tools check again before they return.
- **The rate limit is counted in each process's memory.** `serve` and each `mcp stdio` process count
  apart, and a restart starts the count again.
- **The session cookie is bound to its host, not its port.** A browser also sends it to another
  program that serves the same host.
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

Each is true of the code today.

1. **The Slack start page shows in the opener's arguments.** `debug slack connect` passes the
   one-time start page to the browser opener as an argument. Anyone on the machine who can read the
   opener's arguments can request the page before the browser does, receive the authorization page
   meant for the operator's browser, and leave the operator's browser a 404.
2. **A lost connection keeps its locks.** When the network to PostgreSQL stops passing bytes during a
   receipt's transaction, the connector gives up after a fixed wait and leaves the receipt pending.
   PostgreSQL keeps that transaction's locks until it notices the connection is gone. The connector
   sets no `idle_in_transaction_session_timeout`, and leaves the server's keepalive settings as they
   are.
3. **Some sign-ins leave no receipt.** `connect salesforce` signs in and reads the org's identity, and
   outside Developer Edition the four objects' fields, with no receipt. The Slack sign-in commands
   (`debug slack connect`, `status --check` and `disconnect --revoke`) call Slack with none either.
   Receipts cover tool calls and source reads.
4. **One refusal of `serve` leaves no receipt.** When `serve` cannot read which workspace a bearer was
   issued in, because the database fails or takes more than five seconds, the call is answered HTTP
   503 in plain text and gets no receipt. Nothing runs.
5. **The neutralizer version was not raised after the link changes.** It is still 3, so `inspect`
   shows a unit sealed under the earlier link rules as it was sealed, not neutralized again.
6. **A bare email address is not neutralized.** `dana@files.example` in a quotation stays as written,
   and a GitHub Markdown client turns it into a mail link. In angle brackets or after `mailto:`, it is
   neutralized.
7. **An unused credential.** `setup` writes an `owner-secret` file, and a backup carries it, but no
   command reads it.

## Not verified

No live org has been verified. Every test runs against an invented Salesforce org, an invented `sf`
and an invented Slack, with a local PostgreSQL.

- Sandbox and production Salesforce behaviour has only invented answers behind it.
- No Slack workspace or Slack app has been signed in to.
- No call provider is reached: transcripts are files the operator imports.
- The only MCP client run against the binary is the Inspector CLI, in CI. No assistant app has been
  tested.
- Cards were tried on invented deals only. Nothing here shows that a card is useful to a seller.
