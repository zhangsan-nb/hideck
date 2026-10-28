package api

import (
	"net/http"
	"slices"
	"strings"

	"github.com/yibaiba/hideck/internal/db"
)

// All transports take the same MBN ownership read lease before domain locks.
func (s *Server) acquireBusinessMutation(c businessContext) (func(), bool) {
	if s.pool == nil {
		return func() {}, true
	}
	ids := []string{}
	if id := deviceIDParam(c); id != "" {
		ids = append(ids, id)
	}
	if iccid := db.CanonicalICCID(c.Param("iccid")); iccid != "" {
		for _, worker := range s.pool.GetAllWorkers() {
			if db.CanonicalICCID(worker.CurrentICCID()) == iccid {
				ids = append(ids, worker.ID)
			}
		}
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	releases := []func(){}
	releaseAll := func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}
	for _, id := range ids {
		release, err := s.pool.AcquireDeviceOperation(id)
		if err != nil {
			releaseAll()
			c.JSON(http.StatusConflict, map[string]any{"error": err.Error(), "code": "MBN_BUSY"})
			return nil, false
		}
		releases = append(releases, release)
	}
	return releaseAll, true
}
func (s *Server) isSharedBusinessRoute(path string) bool {
	for _, operation := range s.mcpOperations() {
		candidate := "/api" + mcpPathParameter.ReplaceAllString(operation.path, ":$1")
		if candidate == path {
			return true
		}
	}
	return strings.HasPrefix(path, "/api/mcp/") || path == "/api/settings/mcp"
}
