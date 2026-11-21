// The addresses of the two pages that read from the server. The Go server
// sends "/", /ops and /deals, with anything below them, to the app.
export const serverPaths = {
  evidence: '/ops/evidence',
  readiness: '/ops/readiness',
};
