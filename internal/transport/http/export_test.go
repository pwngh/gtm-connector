package httptransport

// APIRoutes gives the patterns of every /api/v1 route, the writes apart.
func APIRoutes(s *Server) (reads, writes []string) {
	for _, r := range s.apiRoutes() {
		if r.write != nil {
			writes = append(writes, r.pattern)
		} else {
			reads = append(reads, r.pattern)
		}
	}
	return reads, writes
}
