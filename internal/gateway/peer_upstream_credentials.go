package gateway

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// settingPeerUpstreamCredentials stores downstream-side "my account on the
// parent gateway" credentials. It is intentionally a separate settings map:
// Provider.PeerGrants is copied into console state and provider exports, while
// the login password must only be returned by the dedicated manage-only
// credentials endpoint. Keeping the password out of that broader object makes
// accidental disclosure much harder.
const settingPeerUpstreamCredentials = "peerUpstreamCredentials"

type peerUpstreamCredential struct {
	Username  string `json:"username"`
	Password  string `json:"password"`
	ParentURL string `json:"parentUrl,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

func clonePeerUpstreamCredentials(src map[string]peerUpstreamCredential) map[string]peerUpstreamCredential {
	out := make(map[string]peerUpstreamCredential, len(src))
	for key, value := range src {
		out[key] = value
	}
	return out
}

// loadPeerUpstreamCredentialsLocked lazily loads the settings JSON once.
// Runtime always supplies an AdminAuthStore. Tests may omit it; in that case an
// in-memory map still supports the full registration/reconnect flow.
func (s *Server) loadPeerUpstreamCredentialsLocked() error {
	if s.peerCredentialsLoaded {
		return nil
	}
	s.peerCredentials = map[string]peerUpstreamCredential{}
	s.peerCredentialsLoaded = true
	if s.adminAuth == nil {
		return nil
	}
	raw := strings.TrimSpace(s.adminAuth.Setting(settingPeerUpstreamCredentials))
	if raw == "" {
		return nil
	}
	var stored map[string]peerUpstreamCredential
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return fmt.Errorf("parse peer upstream credentials: %w", err)
	}
	if stored != nil {
		s.peerCredentials = stored
	}
	return nil
}

func (s *Server) persistPeerUpstreamCredentialsLocked(next map[string]peerUpstreamCredential) error {
	if s.adminAuth != nil {
		raw, err := json.Marshal(next)
		if err != nil {
			return err
		}
		if err := s.adminAuth.SetSetting(settingPeerUpstreamCredentials, string(raw)); err != nil {
			return err
		}
	}
	s.peerCredentials = next
	s.peerCredentialsLoaded = true
	return nil
}

func (s *Server) peerUpstreamCredentialsSnapshot() map[string]peerUpstreamCredential {
	s.peerCredentialsMu.Lock()
	defer s.peerCredentialsMu.Unlock()
	if err := s.loadPeerUpstreamCredentialsLocked(); err != nil {
		if s.logs != nil {
			s.logs.AddApp("warn", "peer upstream credentials unavailable", err.Error())
		}
		return map[string]peerUpstreamCredential{}
	}
	return clonePeerUpstreamCredentials(s.peerCredentials)
}

func (s *Server) peerUpstreamCredential(upstreamKey string) (peerUpstreamCredential, bool) {
	upstreamKey = strings.TrimSpace(upstreamKey)
	if upstreamKey == "" {
		return peerUpstreamCredential{}, false
	}
	all := s.peerUpstreamCredentialsSnapshot()
	credential, ok := all[upstreamKey]
	return credential, ok
}

func (s *Server) setPeerUpstreamCredential(upstreamKey, parentURL, username, password string) error {
	upstreamKey = strings.TrimSpace(upstreamKey)
	username = strings.TrimSpace(username)
	if upstreamKey == "" {
		return fmt.Errorf("peer upstream key is required")
	}
	if username == "" || strings.TrimSpace(password) == "" {
		return fmt.Errorf("peer upstream username and password are required")
	}
	s.peerCredentialsMu.Lock()
	defer s.peerCredentialsMu.Unlock()
	if err := s.loadPeerUpstreamCredentialsLocked(); err != nil {
		return err
	}
	next := clonePeerUpstreamCredentials(s.peerCredentials)
	next[upstreamKey] = peerUpstreamCredential{
		Username:  username,
		Password:  password,
		ParentURL: strings.TrimSpace(parentURL),
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	return s.persistPeerUpstreamCredentialsLocked(next)
}

func (s *Server) deletePeerUpstreamCredential(upstreamKey string) error {
	upstreamKey = strings.TrimSpace(upstreamKey)
	if upstreamKey == "" {
		return nil
	}
	s.peerCredentialsMu.Lock()
	defer s.peerCredentialsMu.Unlock()
	if err := s.loadPeerUpstreamCredentialsLocked(); err != nil {
		return err
	}
	if _, ok := s.peerCredentials[upstreamKey]; !ok {
		return nil
	}
	next := clonePeerUpstreamCredentials(s.peerCredentials)
	delete(next, upstreamKey)
	return s.persistPeerUpstreamCredentialsLocked(next)
}
