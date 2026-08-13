// Copyright (c) 2026 Nokia. All rights reserved.

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nokia-Bell-Labs/declarative-agents/magefiles/appmanifest"
)

func applicationRoot(t *testing.T) string {
	t.Helper()
	working, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(working)
}

func loadManifest(t *testing.T) appmanifest.Manifest {
	t.Helper()
	manifest, err := loadApplicationManifest(applicationRoot(t))
	if err != nil {
		t.Fatalf("load application manifest: %v", err)
	}
	return manifest
}

// TestManifestClaimsNoRunnableCapability pins the honesty of the manifest: an
// audit-only module ships no agents, so every capability that would imply
// executable behavior stays planned.
func TestManifestClaimsNoRunnableCapability(t *testing.T) {
	manifest := loadManifest(t)
	if manifest.ModuleStatus != "audit_only" {
		t.Fatalf("module_status = %q, want audit_only", manifest.ModuleStatus)
	}
	for name, capability := range manifest.Capabilities {
		switch capability.Status {
		case "planned", "not_applicable":
		default:
			t.Errorf("capability %s = %q; the module has no evidence for anything else",
				name, capability.Status)
		}
	}
	if _, declared := manifest.Capabilities["runnable_module"]; !declared {
		t.Error("manifest does not declare the runnable_module capability")
	}
}

// TestReservedRootsRemainPlanned fails in both directions: a root marked
// planned whose profile exists, and a root not marked planned whose profile is
// absent. The second is what stops a manifest from naming a program that was
// never written.
func TestReservedRootsRemainPlanned(t *testing.T) {
	root := applicationRoot(t)
	manifest := loadManifest(t)
	if err := validateSwarmManifest(root, manifest); err != nil {
		t.Fatalf("validate manifest: %v", err)
	}
	if len(manifest.Roots) != len(plannedRoots) {
		t.Fatalf("roots = %d, want %d", len(manifest.Roots), len(plannedRoots))
	}
	for _, entry := range manifest.Roots {
		if !entry.Planned {
			t.Errorf("root %s is executable; no profile ships yet", entry.ID)
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(entry.Source))); err == nil {
			t.Errorf("root %s source %s exists but the manifest marks it planned", entry.ID, entry.Source)
		}
	}
}

// TestStatsContributesNoAgents pins the accounting: while both roots are
// planned, this module adds nothing to the repository-wide agent total.
func TestStatsContributesNoAgents(t *testing.T) {
	root := applicationRoot(t)
	stats, err := newStats(root, loadManifest(t))
	if err != nil {
		t.Fatalf("compute stats: %v", err)
	}
	if stats.Application.AgentsContributed != 0 {
		t.Errorf("agents_contributed = %d, want 0", stats.Application.AgentsContributed)
	}
	if len(stats.Application.ExecutableRoots) != 0 {
		t.Errorf("executable roots = %v, want none", stats.Application.ExecutableRoots)
	}
	if len(stats.Application.PlannedRoots) != len(plannedRoots) {
		t.Errorf("planned roots = %v, want %v", stats.Application.PlannedRoots, plannedRoots)
	}
	if stats.Application.Documents == 0 {
		t.Error("stats reports no documents")
	}
}

// TestAuditRejectsAnImplementedReleaseClaim proves the status gate has teeth:
// a road map that claims an implemented release without evidence fails.
func TestAuditRejectsAnImplementedReleaseClaim(t *testing.T) {
	root := t.TempDir()
	source := applicationRoot(t)

	copyInto(t, filepath.Join(source, "docs", "ARCHITECTURE.yaml"),
		filepath.Join(root, "docs", "ARCHITECTURE.yaml"))
	roadmap, err := os.ReadFile(filepath.Join(source, "docs", "road-map.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(string(roadmap),
		"    status: planned", "    status: implemented", 1)
	if tampered == string(roadmap) {
		t.Fatal("road map contains no planned release to tamper with")
	}
	writeInto(t, filepath.Join(root, "docs", "road-map.yaml"), []byte(tampered))

	err = auditStatusClaims(root, loadManifest(t))
	if err == nil {
		t.Fatal("audit accepted a road map claiming an implemented release")
	}
	if !strings.Contains(err.Error(), "no executable evidence") {
		t.Fatalf("error = %v, want the missing-evidence rejection", err)
	}
}

// TestREADMEHasEveryRequiredSection covers srd003-application-consistency R6.1.
func TestREADMEHasEveryRequiredSection(t *testing.T) {
	if err := auditREADME(applicationRoot(t)); err != nil {
		t.Fatal(err)
	}
}

func copyInto(t *testing.T, source, target string) {
	t.Helper()
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	writeInto(t, target, data)
}

func writeInto(t *testing.T, target string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
