package httpapi

// Close stops scheduled work and flushes account feedback after HTTP shutdown.
func (s *Server) Close() error {
	s.survivalMu.Lock()
	s.backgroundCancel()
	s.survivalMu.Unlock()
	s.backgroundWorkers.Wait()
	return s.store.FlushAccounts()
}
