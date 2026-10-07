package panel

import (
	"strings"
	"testing"
)

func TestStandaloneRefusesChangedPrincipalAndAttachment(t *testing.T) {
	original := []byte(`{"bindingId":"binding","identityId":"actor","worktreeRoot":"/work","revision":1}`)
	binding, err := parseStandaloneBinding(original, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, changed := range []string{
		`{"bindingId":"binding","identityId":"other","worktreeRoot":"/work","revision":1}`,
		`{"bindingId":"binding","identityId":"actor","worktreeRoot":"/work","revision":2}`,
	} {
		if _, err := parseStandaloneBinding([]byte(changed), binding.Snapshot); err == nil || !bindingChanged(err.Error()) {
			t.Fatalf("changed connection was admitted: %v", err)
		}
	}
	reordered := []byte(`{"revision":1,"worktreeRoot":"/work","identityId":"actor","bindingId":"binding"}`)
	if _, err := parseStandaloneBinding(reordered, binding.Snapshot); err != nil {
		t.Fatal(err)
	}
}

func TestStandaloneRefusesIncompleteConnection(t *testing.T) {
	if _, err := parseStandaloneBinding([]byte(`{"bindingId":"binding"}`), ""); err == nil {
		t.Fatal("incomplete connection was admitted")
	}
}

func TestStandaloneDoesNotSendUnsupportedSnapshotFlag(t *testing.T) {
	standalone := boundArgs([]string{"workspace", "--view", "tree"}, "status:abc")
	if strings.Contains(strings.Join(standalone, " "), "--connection-snapshot") {
		t.Fatal("standalone sent host-only snapshot flag")
	}
	qualified := boundArgs([]string{"workspace"}, "native-snapshot")
	if strings.Join(qualified, " ") != "workspace --connection-snapshot native-snapshot" {
		t.Fatal("Herdr qualified admission changed")
	}
}
