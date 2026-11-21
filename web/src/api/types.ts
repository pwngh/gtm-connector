// What the Go server sends. Each interface named after a view struct in
// internal/transport/http/session.go or evidence.go mirrors that struct: one
// field per json tag, optional where the tag says omitempty, null where the
// Go field is a pointer. types.test.ts holds the two in step.

/** JSON as the server stores it and sends it unread. */
export type Json = null | boolean | number | string | Json[] | {[key: string]: Json};

export interface PrincipalView {
  id: string;
  display_name: string;
  kind: string;
  workspace_id: string;
  workspace_kind: string;
  demo: boolean;
}

export interface SessionView {
  signed_in: boolean;
  principal: PrincipalView | null;
  csrf_token?: string;
  expires_at?: string;
}

export interface SignInRequest {
  login: string;
  password: string;
}

export interface AccountView {
  account: string;
  name: string;
}

export interface CardView {
  id: string;
  account: string;
  brief_id: string;
  kind: string;
  schema_version: number;
  rules_version: string | null;
  sha256: string;
  size: number;
  seal_receipt_id: string;
  published_at: string;
}

export interface CardDetail extends CardView {
  as_of: string | null;
  sources: Record<string, string> | null;
  units: UnitView[] | null;
  gaps: GapView[] | null;
  lines: Json[] | null;
}

/**
 * A sealed unit as the card holds it. Its keys are documented, not checked
 * by the server, so every reader treats each one as possibly absent.
 */
export interface UnitBody {
  n?: number;
  kind?: string;
  source?: string;
  record_ref?: string;
  tool?: string;
  at?: string;
  speaker?: string | null;
  role?: string | null;
  basis?: string | null;
  contact?: number | null;
  text?: string;
  partial?: boolean;
  shown?: string;
}

export interface UnitView {
  card: string;
  n: number;
  kind: string;
  record_ref: string;
  tool: string;
  shown?: string;
  receipt_id: string | null;
  record_version_id: string | null;
  body: Json;
  receipt?: ReceiptView;
}

export interface GapView {
  code: string;
  disposition: string;
  count: number | null;
  by_role: Json;
  text: string;
}

export interface ReceiptView {
  id: string;
  operation_id: string;
  parent_id: string | null;
  ordinal: number | null;
  principal_id: string | null;
  token_id: string;
  tool: string;
  scope: string | null;
  account: string;
  connection_generation: number | null;
  arguments: Json;
  request_id: Json;
  brief_id: string | null;
  status: string;
  http_status: number | null;
  row_count: number;
  rows: string[] | null;
  elapsed_ms: number | null;
  error: string | null;
  started_at: string;
  finished_at: string | null;
  delivered_at: string | null;
}

/** GET /api/v1/accounts */
export interface AccountList {
  accounts: AccountView[];
}

/** GET /api/v1/accounts/{account}/cards */
export interface AccountCards {
  account: string;
  cards: CardView[];
}

/** GET /healthz, a map in Go: 200 when status is "ok", 503 otherwise. */
export interface Health {
  status: string;
}

/** The body of every refusal. */
export interface ErrorBody {
  error: {code: string; message: string};
}
