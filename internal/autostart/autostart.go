// Package autostart owns the macOS login-time record for the desktop app.
//
// The system record is the source of truth, not a boolean in the gateway
// database: the Settings switch reads the LaunchAgent the way macOS will act
// on it, so it cannot say "on" while System Settings has the item switched
// off. The desktop app is launched at login; the headless CLI is unchanged.
package autostart

// Status is the system's current answer for the desktop app's login item.
type Status struct {
	// Available is false outside a macOS .app bundle (the headless CLI, tests,
	// and non-Darwin builds). The UI hides the row in that case.
	Available bool `json:"available"`
	// Enabled means the LaunchAgent record exists and is structurally usable.
	// It can still be held back by the user in System Settings; see NeedsApproval.
	Enabled bool `json:"enabled"`
	// NeedsApproval means the record exists but macOS says it is switched off in
	// System Settings > General > Login Items > Allow in the Background.
	NeedsApproval bool `json:"needsApproval,omitempty"`
	// Mechanism names where the record lives. Kept in the API so the UI can be
	// explicit instead of treating this like a database preference.
	Mechanism string `json:"mechanism,omitempty"`
	// AppPath is the .app bundle the record points at (privacy-safe local path).
	AppPath string `json:"appPath,omitempty"`
	// Message carries a concrete error/warning without turning the row into a
	// misleading "off": the system state remains independently visible.
	Message string `json:"message,omitempty"`
}

// Manager reads and writes the operating system's login-time startup record.
type Manager interface {
	Status() Status
	Set(enabled bool) (Status, error)
	OpenLoginItems() error
	// EnsureDefault applies the first-install default exactly once. A marker
	// outside the login record makes a later user choice to turn it off stick.
	EnsureDefault() error
}

// New returns the platform manager. Only macOS app bundles can be available;
// other platforms return an explicit unavailable status.
func New() Manager { return newManager() }
