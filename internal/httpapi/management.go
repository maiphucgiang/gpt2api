package httpapi

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (s *Server) proxyProfiles(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	cfg, _ := s.store.Config()
	profiles := mapList(cfg["proxy_profiles"])
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, 200, map[string]any{"profiles": profiles})
	case http.MethodPost:
		var request map[string]any
		if !decodeJSON(w, r, &request) {
			return
		}
		id := slugID(stringValue(request["id"]))
		if id == "" {
			id = fmt.Sprintf("proxy-%d", time.Now().UnixNano())
		}
		request["id"] = id
		next := make([]map[string]any, 0, len(profiles))
		replaced := false
		for _, item := range profiles {
			if stringValue(item["id"]) == id {
				next = append(next, request)
				replaced = true
			} else {
				next = append(next, item)
			}
		}
		if !replaced {
			next = append(next, request)
		}
		updated, err := s.store.UpdateConfig("proxy_profiles", next)
		if err != nil {
			writeError(w, 500, err.Error(), "server_error")
			return
		}
		writeJSON(w, 200, map[string]any{"profile": request, "profiles": mapList(updated["proxy_profiles"])})
	default:
		writeError(w, 405, "method not allowed", "invalid_request_error")
	}
}

func (s *Server) proxyGroups(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	cfg, _ := s.store.Config()
	groups := mapList(cfg["proxy_groups"])
	if r.Method == http.MethodGet {
		writeJSON(w, 200, map[string]any{"groups": groups})
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, 405, "method not allowed", "invalid_request_error")
		return
	}
	var request map[string]any
	if !decodeJSON(w, r, &request) {
		return
	}
	id := slugID(stringValue(request["id"]))
	if id == "" {
		id = fmt.Sprintf("group-%d", time.Now().UnixNano())
	}
	request["id"] = id
	next := make([]map[string]any, 0, len(groups))
	found := false
	for _, item := range groups {
		if stringValue(item["id"]) == id {
			next = append(next, request)
			found = true
		} else {
			next = append(next, item)
		}
	}
	if !found {
		next = append(next, request)
	}
	updated, err := s.store.UpdateConfig("proxy_groups", next)
	if err != nil {
		writeError(w, 500, err.Error(), "server_error")
		return
	}
	if err := s.refreshProxyRuntime(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "server_error")
		return
	}
	writeJSON(w, 200, map[string]any{"group": request, "groups": mapList(updated["proxy_groups"])})
}

func (s *Server) proxyHealth(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	writeJSON(w, 200, map[string]any{"status": "ok", "runtime": "go", "checked_at": time.Now().UTC(), "proxy": s.proxyManager.Snapshot()})
}

func localFiles(root, kind string) []map[string]any {
	entries, _ := os.ReadDir(root)
	result := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		result = append(result, map[string]any{"id": strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())), "name": entry.Name(), "kind": kind, "bytes": info.Size(), "updated_at": info.ModTime()})
	}
	return result
}
func deleteLocalMedia(root, id string) int {
	entries, _ := os.ReadDir(root)
	count := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), id+".") {
			if os.Remove(filepath.Join(root, entry.Name())) == nil {
				count++
			}
		}
	}
	return count
}
