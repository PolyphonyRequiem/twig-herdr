package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PolyphonyRequiem/twig-herdr/internal/panel"
)

func TestNewPanelPlacementPreservesHeightAndWidthBudget(t *testing.T) {
	for height := 4; height <= 120; height++ {
		for width := 30; width <= 240; width++ {
			direction, rows := choosePlacement(rectangle{Width: width, Height: height})
			if rows < 1 || rows > 25 {
				t.Fatalf("%dx%d: panel outside row budget: %d", width, height, rows)
			}
			if direction == "right" && (width < 166 || rows != height) {
				t.Fatalf("%dx%d: right split violates usable columns or height", width, height)
			}
			if direction == "down" && rows >= height {
				t.Fatalf("%dx%d: no space left for caller", width, height)
			}
		}
	}
}

func TestPanelRegistryIsScopedToSessionAndTab(t *testing.T) {
	t.Setenv("TWIG_HERDR_STATE_DIR", t.TempDir())
	t.Setenv("HERDR_SOCKET_PATH", "/session-a.sock")
	first := paneInfo{WorkspaceID: "w1", TabID: "w1:t1"}
	second := paneInfo{WorkspaceID: "w1", TabID: "w1:t2"}
	a, _ := registrationPath(first)
	b, _ := registrationPath(second)
	t.Setenv("HERDR_SOCKET_PATH", "/session-b.sock")
	c, _ := registrationPath(first)
	if a == b || a == c || b == c {
		t.Fatal("independent Herdr tabs or sessions share control identity")
	}
}

func TestUnauthenticatedLocalRequestCannotReachPanel(t *testing.T) {
	t.Setenv("TWIG_HERDR_STATE_DIR", t.TempDir())
	t.Setenv("HERDR_SOCKET_PATH", "/security-test.sock")
	source := paneInfo{WorkspaceID: "w1", TabID: "w1:t1", PaneID: "w1:p1", Cwd: t.TempDir()}
	requests := make(chan panel.Request, 1)
	stop, err := servePanel(source, requests)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	file, _ := registrationPath(source)
	record, err := readRegistration(file)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Post("http://"+record.Address+"/control", "application/json", strings.NewReader(`{"command":"close"}`))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized control returned %d", response.StatusCode)
	}
	select {
	case <-requests:
		t.Fatal("unauthenticated request reached the terminal event loop")
	default:
	}

	// Cleanup from an old process cannot unregister its successor.
	replacement := record
	replacement.Token = strings.Repeat("b", 64)
	data, _ := json.Marshal(replacement)
	if err := os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}
	stop()
	if _, err := os.Stat(filepath.Clean(file)); err != nil {
		t.Fatalf("old process removed its successor: %v", err)
	}
}
