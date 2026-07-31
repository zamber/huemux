package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zamber/huemux/internal/appconfig"
	"github.com/zamber/huemux/internal/config"
)

func cfgWithProfile(p appconfig.Profile) appconfig.Config {
	c := appconfig.Default()
	c.Profile = p
	return c
}

// TestBuildPairedRespectsProfile is the regression guard for the trap this
// phase existed to fix: runPair used to construct both services
// unconditionally, so pairing from the web UI silently switched a
// profile-disabled half of the app back on. Both the startup path and the
// pairing path now go through BuildPaired, so this one test covers both.
func TestBuildPairedRespectsProfile(t *testing.T) {
	bridge := config.Bridge{BridgeIP: "192.0.2.10", Username: "u", ClientKey: "k"}

	tests := []struct {
		profile          appconfig.Profile
		wantEng, wantLts bool
	}{
		{appconfig.ProfileFull, true, true},
		{appconfig.ProfileLights, false, true},
		// Sync keeps lightctl: the sync page's scene strip needs /api/scenes.
		{appconfig.ProfileSync, true, true},
	}
	for _, tt := range tests {
		t.Run(string(tt.profile), func(t *testing.T) {
			eng, lights := BuildPaired(cfgWithProfile(tt.profile), bridge, nil, nil)
			if got := eng != nil; got != tt.wantEng {
				t.Errorf("engine constructed = %v, want %v", got, tt.wantEng)
			}
			if got := lights != nil; got != tt.wantLts {
				t.Errorf("lightctl constructed = %v, want %v", got, tt.wantLts)
			}
		})
	}
}

// TestAreasRouteIsProfileGated checks that a lights-only server does not even
// register the entertainment-areas endpoint. /api/lights stays registered
// under every profile, since the sync page depends on the light-control
// endpoints for its scene strip.
func TestAreasRouteIsProfileGated(t *testing.T) {
	tests := []struct {
		profile      appconfig.Profile
		path         string
		wantNotFound bool
	}{
		{appconfig.ProfileLights, "/api/areas", true},
		{appconfig.ProfileFull, "/api/areas", false},
		{appconfig.ProfileSync, "/api/areas", false},
		{appconfig.ProfileLights, "/api/lights", false},
		{appconfig.ProfileSync, "/api/lights", false},
		{appconfig.ProfileLights, "/api/scenes", false},
	}
	for _, tt := range tests {
		t.Run(string(tt.profile)+tt.path, func(t *testing.T) {
			s := New(cfgWithProfile(tt.profile), nil, nil, nil, nil)
			rec := httptest.NewRecorder()
			s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))

			// An unregistered path falls through to the "/" file-server
			// handler, which 404s for a missing embedded file.
			gotNotFound := rec.Code == http.StatusNotFound
			if gotNotFound != tt.wantNotFound {
				t.Errorf("GET %s under profile %s: status %d (notFound=%v), want notFound=%v",
					tt.path, tt.profile, rec.Code, gotNotFound, tt.wantNotFound)
			}
		})
	}
}

// TestPairedReportsBridgeStateNotEngineState guards the CLI readout bug found
// while wiring this up: a lights-only profile has a nil engine forever, so
// inferring "not paired" from a nil engine told an already-paired, correctly
// working server to go and pair itself, on every render tick.
func TestPairedReportsBridgeStateNotEngineState(t *testing.T) {
	s := New(cfgWithProfile(appconfig.ProfileLights), nil, nil, nil, nil)
	if s.Paired() {
		t.Error("fresh server with no services must report unpaired")
	}

	_, lights := BuildPaired(cfgWithProfile(appconfig.ProfileLights),
		config.Bridge{BridgeIP: "192.0.2.10"}, nil, nil)
	s.setPaired(nil, lights)

	if s.Engine() != nil {
		t.Fatal("lights profile must not have an engine")
	}
	if !s.Paired() {
		t.Error("a lights-only server with a paired bridge must report paired despite the nil engine")
	}
}
