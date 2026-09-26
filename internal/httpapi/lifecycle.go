package httpapi

import "errors"

// Close stops scheduled work and flushes account feedback after HTTP shutdown.
func (s *Server) Close() error {
	s.survivalMu.Lock()
	s.backgroundCancel()
	s.survivalMu.Unlock()
	imageErr := s.closeImageTasks()
	s.backgroundWorkers.Wait()
	return errors.Join(imageErr, s.store.FlushAccounts())
}
