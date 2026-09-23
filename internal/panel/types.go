package panel

// Config selects a source workspace and its initial presentation.
type Config struct {
	Cwd         string
	InitialView string
}

// Request is delivered to the terminal-owning event loop. Commands never apply changes.
type Request struct {
	Command string
	View    string
	File    string
	Reply   chan Result
}

type Snapshot struct {
	Mode       string `json:"mode"`
	View       string `json:"view"`
	ReviewFile string `json:"file,omitempty"`
	Ready      bool   `json:"ready"`
}

type Result struct {
	Snapshot Snapshot
	Err      error
}
