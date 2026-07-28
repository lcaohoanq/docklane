package domain

type TraefikRuntimeComponent struct {
	Name    string   `json:"name"`
	Present bool     `json:"present"`
	Status  string   `json:"status,omitempty"`
	Errors  []string `json:"errors,omitempty"`
}

type TraefikRouteRuntime struct {
	Providers    []string                `json:"providers"`
	Router       TraefikRuntimeComponent `json:"router"`
	Service      TraefikRuntimeComponent `json:"service"`
	ServerStatus map[string]string       `json:"serverStatus,omitempty"`
}
