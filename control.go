package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/PolyphonyRequiem/twig-herdr/internal/panel"
)

const pluginID = "twig-herdr"

var errNoPanel = errors.New("no native Twig panel is open in this tab")

type paneInfo struct {
	PaneID      string `json:"pane_id"`
	WorkspaceID string `json:"workspace_id"`
	TabID       string `json:"tab_id"`
	Cwd         string `json:"cwd"`
	Foreground  string `json:"foreground_cwd"`
	Label       string `json:"label"`
}

type rectangle struct{ X, Y, Width, Height int }

type paneLayoutInfo struct {
	Panes []struct {
		PaneID string    `json:"pane_id"`
		Rect   rectangle `json:"rect"`
	} `json:"panes"`
}

func executeJSON(binary, cwd string, args []string, target any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = cwd
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", binary, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	if err := json.Unmarshal(output, target); err != nil {
		return fmt.Errorf("%s returned invalid JSON: %w", binary, err)
	}
	return nil
}

func herdr(args []string, result any) error {
	binary := os.Getenv("HERDR_BIN_PATH")
	if binary == "" {
		binary = "herdr"
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := executeJSON(binary, "", args, &envelope); err != nil {
		return err
	}
	if envelope.Error != nil {
		return errors.New(envelope.Error.Message)
	}
	if len(envelope.Result) == 0 {
		return errors.New("Herdr returned no result")
	}
	if result == nil {
		return nil
	}
	return json.Unmarshal(envelope.Result, result)
}

func resolvePane(id string) (paneInfo, error) {
	if id == "" {
		id = os.Getenv("HERDR_PANE_ID")
	}
	if id == "" {
		return paneInfo{}, errors.New("HERDR_PANE_ID or --pane is required")
	}
	// Native pane processes receive a stable source cwd even after being focused.
	if id == os.Getenv("HERDR_PANE_ID") && os.Getenv("TWIG_PANEL_CWD") != "" && os.Getenv("HERDR_TAB_ID") != "" && os.Getenv("HERDR_WORKSPACE_ID") != "" {
		return paneInfo{PaneID: id, WorkspaceID: os.Getenv("HERDR_WORKSPACE_ID"), TabID: os.Getenv("HERDR_TAB_ID"), Cwd: os.Getenv("TWIG_PANEL_CWD")}, nil
	}
	var response struct {
		Pane paneInfo `json:"pane"`
	}
	if err := herdr([]string{"pane", "get", id}, &response); err != nil {
		return paneInfo{}, err
	}
	p := response.Pane
	if p.Foreground != "" {
		p.Cwd = p.Foreground
	}
	if p.PaneID == "" || p.TabID == "" || p.WorkspaceID == "" || p.Cwd == "" {
		return paneInfo{}, errors.New("source pane has incomplete workspace identity")
	}
	return p, nil
}

func checkTwig(cwd string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "twig", "--version")
	cmd.Dir = cwd
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("Twig 0.94.0+ is required on PATH: %w", err)
	}
	fields := strings.Fields(string(output))
	if len(fields) == 0 {
		return errors.New("Twig returned no version")
	}
	parts := strings.Split(strings.TrimPrefix(fields[len(fields)-1], "v"), ".")
	if len(parts) < 3 {
		return fmt.Errorf("unrecognized Twig version %q", string(output))
	}
	major, errMajor := strconv.Atoi(parts[0])
	minor, errMinor := strconv.Atoi(parts[1])
	if errMajor != nil || errMinor != nil || major < 0 || (major == 0 && minor < 94) {
		return fmt.Errorf("Twig 0.94.0+ required; found %s. Run twig upgrade", strings.TrimSpace(string(output)))
	}
	return nil
}

type registration struct {
	Address string `json:"address"`
	Token   string `json:"token"`
	PaneID  string `json:"paneId"`
	Cwd     string `json:"cwd"`
}

type controlRequest struct {
	Command string `json:"command"`
	View    string `json:"view,omitempty"`
	File    string `json:"file,omitempty"`
}

type controlResponse struct {
	panel.Snapshot
	PaneID string `json:"paneId,omitempty"`
	Error  string `json:"error,omitempty"`
}

func registrationPath(source paneInfo) (string, error) {
	root := os.Getenv("TWIG_HERDR_STATE_DIR")
	if root == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			return "", err
		}
		root = filepath.Join(cache, pluginID, "panels")
	}
	identity := os.Getenv("HERDR_SOCKET_PATH") + "\x00" + source.WorkspaceID + "\x00" + source.TabID
	sum := sha256.Sum256([]byte(identity))
	return filepath.Join(root, hex.EncodeToString(sum[:16])+".json"), nil
}

func readRegistration(file string) (registration, error) {
	data, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return registration{}, errNoPanel
	}
	if err != nil {
		return registration{}, err
	}
	var record registration
	if err := json.Unmarshal(data, &record); err != nil {
		return registration{}, fmt.Errorf("invalid panel registration: %w", err)
	}
	host, _, err := net.SplitHostPort(record.Address)
	if err != nil || host != "127.0.0.1" || len(record.Token) != 64 {
		return registration{}, errors.New("invalid local panel endpoint")
	}
	return record, nil
}

func requestPanel(record registration, command controlRequest) (controlResponse, error) {
	data, err := json.Marshal(command)
	if err != nil {
		return controlResponse{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+record.Address+"/control", bytes.NewReader(data))
	if err != nil {
		return controlResponse{}, err
	}
	req.Header.Set("Authorization", "Bearer "+record.Token)
	// Do not send local control traffic through a configured network proxy.
	client := &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
	response, err := client.Do(req)
	if err != nil {
		return controlResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return controlResponse{}, fmt.Errorf("panel control returned HTTP %d", response.StatusCode)
	}
	var result controlResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 65536)).Decode(&result); err != nil {
		return result, err
	}
	if result.Error != "" {
		return result, errors.New(result.Error)
	}
	return result, nil
}

func discardOwnRegistration(file, token string) {
	record, err := readRegistration(file)
	if err == nil && record.Token == token {
		_ = os.Remove(file)
	}
}

func servePanel(source paneInfo, requests chan<- panel.Request) (func(), error) {
	file, err := registrationPath(source)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		listener.Close()
		return nil, err
	}
	record := registration{Address: listener.Addr().String(), Token: hex.EncodeToString(secret), PaneID: source.PaneID, Cwd: source.Cwd}
	registry, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		listener.Close()
		return nil, fmt.Errorf("tab already has a native panel registration; use open to reuse or recover it: %w", err)
	}
	if err := json.NewEncoder(registry).Encode(record); err != nil {
		registry.Close()
		listener.Close()
		os.Remove(file)
		return nil, err
	}
	if err := registry.Close(); err != nil {
		listener.Close()
		os.Remove(file)
		return nil, err
	}
	server := &http.Server{ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 25 * time.Second, IdleTimeout: 3 * time.Second}
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost || req.URL.Path != "/control" {
			http.NotFound(w, req)
			return
		}
		if subtle.ConstantTimeCompare([]byte(req.Header.Get("Authorization")), []byte("Bearer "+record.Token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var input controlRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, req.Body, 65536))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if input.Command != "status" && input.Command != "view" && input.Command != "review" && input.Command != "exit-review" && input.Command != "close" {
			http.Error(w, "invalid command", http.StatusBadRequest)
			return
		}
		reply := make(chan panel.Result, 1)
		request := panel.Request{Command: input.Command, View: input.View, File: input.File, Reply: reply}
		select {
		case requests <- request:
		case <-req.Context().Done():
			return
		}
		select {
		case result := <-reply:
			output := controlResponse{Snapshot: result.Snapshot, PaneID: record.PaneID}
			if result.Err != nil {
				output.Error = result.Err.Error()
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(output)
		case <-req.Context().Done():
		}
	})
	go func() { _ = server.Serve(listener) }()
	return func() { _ = server.Close(); discardOwnRegistration(file, record.Token) }, nil
}

func controlPanel(source paneInfo, command, proposal string) (controlResponse, error) {
	file, err := registrationPath(source)
	if err != nil {
		return controlResponse{}, err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return controlResponse{}, err
	}
	unlock, err := lockLaunch(file + ".lock")
	if err != nil {
		return controlResponse{}, err
	}
	defer unlock()
	record, err := readRegistration(file)
	if err == nil {
		current, pingErr := requestPanel(record, controlRequest{Command: "status"})
		if pingErr == nil {
			if command == "open" || command == "status" {
				return current, nil
			}
			input := controlRequest{Command: command, File: proposal}
			if command == "table" || command == "tree" {
				input.Command, input.View = "view", command
			}
			return requestPanel(record, input)
		}
		// Only a definitively closed listener is stale; never steal from a slow peer.
		var op *net.OpError
		if !errors.As(pingErr, &op) || op.Op != "dial" || op.Timeout() {
			return controlResponse{}, fmt.Errorf("existing panel is not responding: %w", pingErr)
		}
		discardOwnRegistration(file, record.Token)
	} else if !errors.Is(err, errNoPanel) {
		return controlResponse{}, err
	}
	if command == "status" || command == "exit-review" {
		return controlResponse{}, errNoPanel
	}
	if err := checkTwig(source.Cwd); err != nil {
		return controlResponse{}, err
	}
	var bench struct {
		Current string `json:"current"`
	}
	if err := executeJSON("twig", source.Cwd, []string{"bench", "list", "-o", "json"}, &bench); err != nil {
		return controlResponse{}, fmt.Errorf("source workspace is not ready: %w", err)
	}
	if bench.Current == "" {
		return controlResponse{}, errors.New("source workspace has no current bench")
	}

	var existing struct {
		Panes []paneInfo `json:"panes"`
	}
	if err := herdr([]string{"pane", "list", "--workspace", source.WorkspaceID}, &existing); err != nil {
		return controlResponse{}, err
	}
	for _, pane := range existing.Panes {
		if pane.TabID == source.TabID && pane.Label == "Twig bench" {
			return controlResponse{}, fmt.Errorf("Twig bench pane %s is already present but not reachable; close that old panel before opening its replacement", pane.PaneID)
		}
	}
	initialView := "tree"
	if command == "table" {
		initialView = "table"
	}
	created, err := launchPanel(source, initialView, proposal)
	if err != nil {
		return controlResponse{}, err
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		record, err := readRegistration(file)
		if err == nil {
			result, err := requestPanel(record, controlRequest{Command: "status"})
			if err == nil && result.Ready {
				return result, nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if cleanupErr := closePanel(created); cleanupErr != nil {
		return controlResponse{}, fmt.Errorf("panel %s did not become ready; cleanup failed: %w", created, cleanupErr)
	}
	return controlResponse{}, fmt.Errorf("panel %s did not become ready; it was closed", created)
}

func layoutFor(id string) (rectangle, error) {
	var response struct {
		Layout paneLayoutInfo `json:"layout"`
	}
	if err := herdr([]string{"pane", "layout", "--pane", id}, &response); err != nil {
		return rectangle{}, err
	}
	for _, pane := range response.Layout.Panes {
		if pane.PaneID == id {
			return pane.Rect, nil
		}
	}
	return rectangle{}, errors.New("pane is absent from its layout")
}
func closePanel(id string) error {
	if id == "" {
		return nil
	}
	return herdr([]string{"pane", "close", id}, nil)
}

func choosePlacement(rect rectangle) (string, int) {
	if rect.Width >= 166 && rect.Height <= 25 {
		return "right", rect.Height
	}
	// Leave twelve total rows for the caller where possible. A short terminal
	// uses a half split and reports the content-space shortfall rather than lying.
	height := min(25, rect.Height-12)
	if height < 17 {
		height = max(1, rect.Height/2)
	}
	return "down", height
}

func launchPanel(source paneInfo, view, review string) (string, error) {
	rect, err := layoutFor(source.PaneID)
	if err != nil {
		return "", err
	}
	direction, height := choosePlacement(rect)
	entrypoint := "bench"
	if runtime.GOOS == "windows" {
		entrypoint = "bench-windows"
	}
	args := []string{"plugin", "pane", "open", "--plugin", pluginID, "--entrypoint", entrypoint, "--target-pane", source.PaneID, "--direction", direction, "--cwd", source.Cwd, "--no-focus"}
	for _, pair := range [][2]string{{"PATH", os.Getenv("PATH")}, {"TWIG_PANEL_CWD", source.Cwd}, {"TWIG_PANEL_INITIAL_VIEW", view}, {"HERDR_SOCKET_PATH", os.Getenv("HERDR_SOCKET_PATH")}, {"HERDR_WORKSPACE_ID", source.WorkspaceID}, {"HERDR_TAB_ID", source.TabID}} {
		args = append(args, "--env", pair[0]+"="+pair[1])
	}
	if review != "" {
		args = append(args, "--env", "TWIG_PANEL_INITIAL_REVIEW_FILE="+review)
	}
	var response struct {
		PluginPane struct {
			Pane paneInfo `json:"pane"`
		} `json:"plugin_pane"`
	}
	if err := herdr(args, &response); err != nil {
		return "", err
	}
	id := response.PluginPane.Pane.PaneID
	if id == "" {
		return "", errors.New("Herdr returned no new panel identity")
	}
	fail := func(err error) (string, error) {
		if closeErr := closePanel(id); closeErr != nil {
			return id, errors.Join(err, fmt.Errorf("close failed: %w", closeErr))
		}
		return id, err
	}
	if direction == "down" {
		actual, err := layoutFor(id)
		if err != nil {
			return fail(err)
		}
		delta := actual.Height - height
		if delta != 0 {
			resize := "down"
			if delta < 0 {
				resize = "up"
				delta = -delta
			}
			amount := strconv.FormatFloat(float64(delta)/float64(rect.Height), 'f', 8, 64)
			if err := herdr([]string{"pane", "resize", "--pane", source.PaneID, "--direction", resize, "--amount", amount}, nil); err != nil {
				return fail(err)
			}
		}
	}
	actual, err := layoutFor(id)
	if err != nil {
		return fail(err)
	}
	if actual.Height > 25 {
		return fail(fmt.Errorf("new panel has %d rows, exceeding its 25-row budget", actual.Height))
	}
	if actual.Height < 17 {
		fmt.Fprintln(os.Stderr, "Small terminal: fewer than ten work-item content rows are available after panel overhead.")
	}
	return id, nil
}
