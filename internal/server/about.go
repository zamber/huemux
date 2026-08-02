package server

import (
	"encoding/json"
	"net/http"
)

// SourceURL is where this build's source can be obtained. GPL-3.0 requires
// that a distributed binary be accompanied by, or offer, its corresponding
// source; for a project that is public on the internet, saying where is the
// practical form of that offer, and the About screen is where a user looks.
const SourceURL = "https://github.com/zamber/huemux"

// LicenseID is the SPDX identifier for HueMux itself. Third-party components
// are covered separately by web/THIRD_PARTY_LICENSES.md, which is generated
// from the modules actually linked in.
const LicenseID = "GPL-3.0-or-later"

type aboutResponse struct {
	Version   string `json:"version"`
	License   string `json:"license"`
	SourceURL string `json:"source_url"`
}

// handleAbout backs the Settings page's About section.
//
// Not folded into /api/config: that endpoint is loopback-gated for writes and
// describes mutable application settings, whereas this is immutable build
// metadata that any client may read. Keeping them apart means the About screen
// works under every profile and auth mode without relaxing anything.
func (s *Server) handleAbout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(aboutResponse{
		Version:   Version,
		License:   LicenseID,
		SourceURL: SourceURL,
	})
}
