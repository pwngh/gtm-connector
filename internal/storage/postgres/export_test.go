package postgres

// SetCrash makes the store fail at a named step inside a transaction, as if
// the process stopped there: the transaction is rolled back.
func SetCrash(s *Store, crash func(point string) error) { s.crash = crash }
