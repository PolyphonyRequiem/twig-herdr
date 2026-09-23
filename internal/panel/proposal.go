package panel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type proposalCandidate struct {
	Found  bool   `json:"found"`
	File   string `json:"file"`
	Digest string `json:"digest"`

	State string `json:"state"`
}

func reviewPreviewArgs(file, digest string) []string {
	args := []string{"proposal", "preview", "--file", file}
	if digest != "" {
		args = append(args, "--expect-digest", digest)
	}
	return append(args, "--interactive")
}

func latestProposal(ctx context.Context, cwd string) (proposalCandidate, error) {
	cmd := exec.CommandContext(ctx, "twig", "proposal", "latest", "-o", "json")
	cmd.Dir = cwd
	output, err := cmd.Output()
	if err != nil {
		var detail string
		if exitErr, ok := err.(*exec.ExitError); ok && len(exitErr.Stderr) != 0 {
			detail = strings.TrimSpace(string(exitErr.Stderr))
		}
		if detail != "" {
			return proposalCandidate{}, fmt.Errorf("resolve latest proposal: %s", detail)
		}
		return proposalCandidate{}, fmt.Errorf("resolve latest proposal: %w", err)
	}
	return resolveLatestProposalResponse(output)
}

func resolveLatestProposalResponse(data []byte) (proposalCandidate, error) {
	candidate, err := parseLatestProposal(data)
	if err != nil {
		return proposalCandidate{}, err
	}
	if !candidate.Found {
		return proposalCandidate{}, errors.New("no unresolved proposal is available")
	}
	return validateProposalCandidate(candidate)
}

func parseLatestProposal(data []byte) (proposalCandidate, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return proposalCandidate{}, errors.New("latest proposal response must be a JSON object")
	}
	var candidate proposalCandidate
	if err := json.Unmarshal(trimmed, &candidate); err != nil {
		return proposalCandidate{}, fmt.Errorf("decode latest proposal response: %w", err)
	}
	if candidate.Found && (strings.TrimSpace(candidate.File) == "" || candidate.Digest == "") {
		return proposalCandidate{}, errors.New("latest proposal response is missing its file or digest")
	}
	return candidate, nil
}

func validateProposalCandidate(candidate proposalCandidate) (proposalCandidate, error) {
	if !candidate.Found {
		return proposalCandidate{}, errors.New("no unresolved proposal is available")
	}
	if !filepath.IsAbs(candidate.File) {
		return proposalCandidate{}, errors.New("latest proposal returned a non-absolute file path")
	}
	digestBytes, err := hex.DecodeString(candidate.Digest)
	if err != nil || len(digestBytes) != sha256.Size || strings.ToLower(candidate.Digest) != candidate.Digest {
		return proposalCandidate{}, errors.New("latest proposal returned an invalid SHA-256 digest")
	}
	info, err := os.Stat(candidate.File)
	if err != nil {
		return proposalCandidate{}, fmt.Errorf("latest proposal file %q is unavailable: %w", candidate.File, err)
	}
	if !info.Mode().IsRegular() {
		return proposalCandidate{}, fmt.Errorf("latest proposal file is not a regular file: %s", candidate.File)
	}
	return candidate, nil
}
