package foxapp

import "testing"

func TestManifestValidateRejectsMissingCommand(t *testing.T) {
	manifest := validManifest()
	manifest.Run.Command = ""
	if err := manifest.Validate(); err == nil {
		t.Fatal("expected missing command error")
	}
}

func TestManifestValidateRejectsInvalidWindowPath(t *testing.T) {
	manifest := validManifest()
	manifest.Window.Path = "topology"
	if err := manifest.Validate(); err == nil {
		t.Fatal("expected invalid window path error")
	}
}

func validManifest() Manifest {
	return Manifest{
		Format:  FormatV1,
		ID:      "topology",
		Name:    "Topology Editor",
		Version: "0.1.0",
		Run:     CommandSpec{Command: "bin/topology"},
		Icon:    IconSpec{Type: "builtin", Value: "network"},
		Window:  WindowSpec{Title: "Topology editor", Path: "/"},
		Health:  HealthSpec{Path: "/healthz"},
		Web:     WebSpec{Dist: "web/dist"},
	}
}
