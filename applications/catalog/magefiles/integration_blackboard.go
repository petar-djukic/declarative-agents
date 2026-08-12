// Copyright (c) 2026 Nokia. All rights reserved.

package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// BlackboardMemory writes a provenance-tagged entry through the shipped
// memory-write block against a live local Chroma and Ollama, then reads it back
// by a metadata filter and by an exact substring of its content. The
// conformance case skips with a recorded reason when either server is absent,
// so this target reports SKIP rather than failing a checkout without the local
// dependencies.
func (Integration) BlackboardMemory() error {
	profilesRoot, err := catalogOwnerRoot("catalog integration:blackboardMemory")
	if err != nil {
		return err
	}
	if err := requireProfilePaths(profilesRoot, "agents/knowledge-manager/memory-write/profile.yaml"); err != nil {
		return err
	}
	cmd := exec.Command("go", "test", "./conformance",
		"-run", "^TestBlackboardMemoryLiveRoundtrip$", "-count=1", "-v", "-live")
	cmd.Dir = profilesRoot
	var transcript bytes.Buffer
	cmd.Stdout = io.MultiWriter(os.Stdout, &transcript)
	cmd.Stderr = io.MultiWriter(os.Stderr, &transcript)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("run shipped blackboard write and filtered read: %w", err)
	}
	// A skipped case is a reported dependency gap, not evidence. Saying PASS for
	// both outcomes is how a target starts claiming proof it never produced.
	if strings.Contains(transcript.String(), "--- SKIP") {
		fmt.Println("integration:blackboardMemory SKIP - the live roundtrip did not run; see the recorded reason above")
		return nil
	}
	fmt.Println("integration:blackboardMemory PASS - shipped memory-write stored a tagged entry retrieved by metadata and by exact substring")
	return nil
}
