// The addresses of the pages that read from the server. The Go server
// sends "/", /ops and /deals, with anything below them, to the app.
export const serverPaths = {
  deals: '/deals',
  coverage: '/ops/coverage',
  activity: '/ops/activity',
};
