package server

import "net/http"

// Distribution upgrades are manual. These authenticated compatibility routes
// reject immediately: no network request, stage/swap, rollback, SSE or restart.
const upstreamUpdateDisabled = "upstream self-update disabled for VulnHarvest; close the application and install a reviewed VulnHarvest package manually"

func (s *Server) updateCheck(w http.ResponseWriter, r *http.Request) {
	writeErr(w, http.StatusForbidden, upstreamUpdateDisabled)
}
func (s *Server) updateApply(w http.ResponseWriter, r *http.Request) {
	writeErr(w, http.StatusForbidden, upstreamUpdateDisabled)
}
func (s *Server) updateRollback(w http.ResponseWriter, r *http.Request) {
	writeErr(w, http.StatusForbidden, upstreamUpdateDisabled)
}
func (s *Server) updateStream(w http.ResponseWriter, r *http.Request) {
	writeErr(w, http.StatusForbidden, upstreamUpdateDisabled)
}
