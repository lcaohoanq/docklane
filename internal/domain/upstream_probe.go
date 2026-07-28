package domain

type UpstreamProbe struct {
	URL        string `json:"url"`
	Reachable  bool   `json:"reachable"`
	HTTPStatus int    `json:"httpStatus,omitempty"`
	DurationMS int64  `json:"durationMs"`
	Error      string `json:"error,omitempty"`
}
