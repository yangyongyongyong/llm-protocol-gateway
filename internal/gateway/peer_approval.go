package gateway

import "fmt"

const settingPeerApprovalRequired = "peerApprovalRequired"

// peerApprovalRequired controls whether a newly registered downstream starts
// disabled until an administrator approves it. The default is false so existing
// deployments keep the historical "register and use immediately" behavior.
func (s *Server) peerApprovalRequired() bool {
	if s == nil || s.adminAuth == nil {
		return false
	}
	value := s.adminAuth.Setting(settingPeerApprovalRequired)
	return value == "1" || value == "true"
}

func (s *Server) setPeerApprovalRequired(required bool) error {
	if s == nil || s.adminAuth == nil {
		return fmt.Errorf("admin settings store is not configured")
	}
	value := "0"
	if required {
		value = "1"
	}
	return s.adminAuth.SetSetting(settingPeerApprovalRequired, value)
}
