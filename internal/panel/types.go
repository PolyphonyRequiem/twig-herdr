package panel

// Config selects a source workspace and its initial presentation.
type Config struct {
	Cwd         string
	InitialView string
	// Standalone uses connection status checks instead of qualified host snapshots.
	Standalone bool
}

// Request is delivered to the terminal-owning event loop. Commands never apply changes.
type Request struct {
	Command string
	View    string
	File    string
	Reply   chan Result
}

type Snapshot struct {
	Mode              string `json:"mode"`
	View              string `json:"view"`
	ReviewFile        string `json:"file,omitempty"`
	Ready             bool   `json:"ready"`
	BindingID         string `json:"bindingId,omitempty"`
	IdentityID        string `json:"identityId,omitempty"`
	ReconnectRequired bool   `json:"reconnectRequired"`
	Error             string `json:"reconnectError,omitempty"`
}

type Result struct {
	Snapshot Snapshot
	Err      error
}
