# Decisions

## Scope and implementation

Use Python 3.11+ and SQLite. The foundation needs only the standard library,
which keeps its clean-checkout command small. No data or code comes from a real
CRM, employer, or client. Seed values are fixed, use stable record IDs, and use
reserved `.example` email domains. Currency values are integer USD cents.

Reseeding must preserve existing content. Missing or extra fixture records are
an error; a new database path is the explicit way to start fresh. Reads use
SQLite read-only connections. Direct CLI inspection is a developer fixture
utility, not a claim of authorization enforcement.

The default briefing adapter will use deterministic fixtures rather than a
paid model. It must still pass the citation checker and evaluation. A hosted
adapter is optional and will not be required by the demo command.

## Four reviewable slices

1. Seeded SQLite CRM, CLI inspection, and data tests.
2. Local authorization and MCP tools, audit records, idempotent activity writes,
   and scope enforcement tests. Run council on this core-logic PR.
3. Brief workflow, citation checker, restricted-scope behavior, and injection test.
4. Twenty-account evaluation, recorded scores, one-page security review, and the
   complete clean-checkout demo.

Each slice must run and pass its tests before full-stop handoff. The user merges.
Only the initial README/license bootstrap goes directly to main. No deployments,
package publishing, real CRM APIs, web UI, or multi-tenant features.

## Protocol research (2026-11-17)

Read the current specification and authorization documents before implementation.
The latest URL resolved to MCP revision 2026-07-28:

- [Specification](https://modelcontextprotocol.io/specification/2026-07-28)
- [Key changes](https://modelcontextprotocol.io/specification/2026-07-28/changelog)
- [Authorization](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization)
- [Discovery](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization/authorization-server-discovery)
- [Client registration](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization/client-registration)
- [Authorization security](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization/security-considerations)

This revision uses stateless requests and server/discover rather than the old
initialization handshake. Before choosing an SDK, check it against the current
transport and schema. Relevant authorization requirements include discovery
metadata, resource-bound tokens, scoped challenges, PKCE, issuer validation,
and HTTPS authorization endpoints. These are research notes; no protocol or
authorization conformance is claimed by the foundation.

## Review configuration

The installed TStack source is [t3dotgg/tstack at 11e37a7](https://github.com/t3dotgg/tstack/tree/11e37a74d0a6868c121cd0910ab2b3bd9873b92a).
Full-stop uses Sol review for each slice. Council runs once on the core-logic PR.
The user selected GPT Sol, Claude Opus, and Gemini 3.8 Flash at High as three full
members. Gemini Flash is not described as a flagship. Record actual model IDs
and scores in the PR table; three council rounds is the hard limit. See
AGENTS.md for the durable rules.

Google's supported personal-account route is Antigravity CLI; legacy Gemini CLI
sign-in is retired. Review tooling uses existing accounts and is separate from
the demo runtime. It must not create accounts, trials, subscriptions, or keys.
