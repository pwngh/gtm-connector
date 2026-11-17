# Decisions

## 2026-11-17: local bootstrap and shipping prerequisites

The repository starts with a README stub and MIT license on main. All later
work belongs on task branches. The current branch is codex/01-mcp-foundation.
There is no remote, push, pull request, or implementation yet.

Installed all nine upstream TStack skills into the local Codex skills directory:
babysit-pr, council, file-pr, full-send, full-stop, leaving-pr-comments,
modernize, sol-loop, and takeover. Source:
[t3dotgg/tstack at 11e37a74d0a6868c121cd0910ab2b3bd9873b92a](https://github.com/t3dotgg/tstack/tree/11e37a74d0a6868c121cd0910ab2b3bd9873b92a).
All 14 installed files match the upstream Git blob hashes. Full-send is an
upstream dependency of full-stop; its merge permission does not apply here.
Only the user may merge.

GitHub CLI is authenticated as pwngh. Native subagents expose OpenAI models,
including GPT Sol. Claude CLI authentication was verified after the user signed
in with an existing subscription. No Gemini CLI or multi-provider delegation
tool is available in this session. Council requires three providers and
prohibits silent substitution. No model inference calls were made. The user's
explicit unavailable-model stop rule therefore keeps the project on a local
branch until the third-provider review route is available.

## Specification research

Read the current MCP specification and authorization pages before implementation.
The latest URL resolved to revision 2026-07-28 on 2026-11-17:

- [Specification](https://modelcontextprotocol.io/specification/2026-07-28)
- [Key changes](https://modelcontextprotocol.io/specification/2026-07-28/changelog)
- [Authorization](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization)
- [Authorization server discovery](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization/authorization-server-discovery)
- [Client registration](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization/client-registration)
- [Authorization security](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization/security-considerations)

This revision uses stateless requests and server/discover rather than the old
initialization handshake. Implementation must be checked against this revision,
including the transport and schema, before choosing an SDK.

Authorization research identified protected-resource and authorization-server
metadata discovery, resource-bound access tokens, scoped challenges, PKCE,
issuer validation, and HTTPS authorization endpoints as relevant requirements.
These are research notes, not claims of implemented conformance.

## Planned implementation choices

Use Python and SQLite, with invented seed data and a deterministic fixture
adapter for the default brief workflow. These avoid a database service and a
paid model key. Decide SDK and local TLS setup after the shipping blocker is
resolved. No real CRM or employer materials will be used.

Proposed four runnable pull-request slices, each stopped for manual merge:

1. Seeded SQLite CRM, CLI inspection, and data tests.
2. Local authorization and MCP tools, audit records, idempotent activity writes,
   and scope enforcement tests. Run council on this core-logic PR.
3. Brief workflow, citation checker, restricted-scope behavior, and injection test.
4. Twenty-account evaluation, recorded measurements, security review, and the
   clean-checkout demo command.

Each slice uses full-stop and its Sol review. Before the first push, inspect
the entire tree and history for secrets and private source material. Create
only a private repository under pwngh, push the bootstrap once to main, and
push later work only to task branches. Do not merge or publish.
