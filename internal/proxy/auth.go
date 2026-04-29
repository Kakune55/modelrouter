package proxy

import (
	"net/http"
	"strings"

	"modelrouter/internal/config"
)

type clientIdentity struct {
	Name            string
	AllowedPatterns []string
	BlockedPatterns []string
	Authenticated   bool
}

func (h *Handler) authenticate(r *http.Request) (clientIdentity, bool) {
	cfg := h.store.Get().Config
	if !cfg.Auth.Enabled {
		return clientIdentity{Name: "anonymous", Authenticated: false}, true
	}

	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		return clientIdentity{}, false
	}
	for _, key := range cfg.Auth.Keys {
		if token != key.Key {
			continue
		}
		identity := clientIdentity{
			Name:          key.Name,
			Authenticated: true,
		}
		if access, ok := cfg.Access[key.Name]; ok {
			identity.AllowedPatterns = append([]string(nil), access.AllowedModels...)
			identity.BlockedPatterns = append([]string(nil), access.BlockedModels...)
		}
		return identity, true
	}
	return clientIdentity{}, false
}

func bearerToken(header string) (string, bool) {
	if strings.TrimSpace(header) == "" {
		return "", false
	}
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.TrimSpace(parts[1]) == "" {
		return "", false
	}
	return parts[1], true
}

func (id clientIdentity) CanAccessModel(model string) bool {
	if !id.Authenticated {
		return true
	}
	if matchAnyPattern(id.BlockedPatterns, model) {
		return false
	}
	if len(id.AllowedPatterns) == 0 {
		return true
	}
	return matchAnyPattern(id.AllowedPatterns, model)
}

func matchAnyPattern(patterns []string, value string) bool {
	for _, pattern := range patterns {
		if wildcardMatch(pattern, value) {
			return true
		}
	}
	return false
}

func wildcardMatch(pattern, value string) bool {
	p, v := 0, 0
	star, match := -1, 0
	for v < len(value) {
		if p < len(pattern) && (pattern[p] == '?' || pattern[p] == value[v]) {
			p++
			v++
			continue
		}
		if p < len(pattern) && pattern[p] == '*' {
			star = p
			match = v
			p++
			continue
		}
		if star != -1 {
			p = star + 1
			match++
			v = match
			continue
		}
		return false
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}

func visibleModels(cfg *config.Config, id clientIdentity) []string {
	names := make([]string, 0, len(cfg.Models))
	for name := range cfg.Models {
		if id.CanAccessModel(name) {
			names = append(names, name)
		}
	}
	return names
}
