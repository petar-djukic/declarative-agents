// Copyright (c) 2026 Nokia. All rights reserved.

package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestApplicationModulesParticipateInAudit proves the application modules are dispatched
// by the root audit gate alongside the platform sub-modules, so a standalone
// application cannot silently drop out of mage audit.
func TestApplicationModulesParticipateInAudit(t *testing.T) {
	participants := auditParticipants()
	for _, mod := range applicationModules {
		if !contains(participants, mod) {
			t.Fatalf("auditParticipants() = %#v, missing application module %q", participants, mod)
		}
	}
	for _, mod := range subModules {
		if !contains(participants, mod) {
			t.Fatalf("auditParticipants() = %#v, missing sub-module %q", participants, mod)
		}
	}
	for _, mod := range auditOnlyApplicationModules {
		if !contains(participants, mod) {
			t.Fatalf("auditParticipants() = %#v, missing audit-only module %q", participants, mod)
		}
	}
}

// TestChatbotMeshIsAnApplicationModule pins the mesh module into the application gate
// so the #476 regression (root gates omitting applications/chatbot-mesh) stays fixed.
func TestChatbotMeshIsAnApplicationModule(t *testing.T) {
	if !contains(applicationModules, "applications/chatbot-mesh") {
		t.Fatalf("applicationModules = %#v, want it to include applications/chatbot-mesh", applicationModules)
	}
}

func TestCodingAgentParticipatesInAudit(t *testing.T) {
	if !contains(auditParticipants(), "applications/coding-agent") {
		t.Fatalf("auditParticipants() = %#v, want coding-agent", auditParticipants())
	}
	if !contains(applicationModules, "applications/coding-agent") {
		t.Fatal("coding-agent owns tests and stats and must participate as an application module")
	}
}

func TestAgentArchitectureIsCompositionApplicationModule(t *testing.T) {
	const module = "applications/agent-architecture"
	if !contains(applicationModules, module) {
		t.Fatalf("applicationModules = %#v, want it to include %s", applicationModules, module)
	}
	if contains(subModules, module) {
		t.Fatalf("subModules = %#v, composition-only demo must remain outside Build and All", subModules)
	}
}

func TestProseEditorIsRunnableButNotBuildManaged(t *testing.T) {
	const module = "applications/prose-editor"
	if contains(auditOnlyApplicationModules, module) {
		t.Fatalf("auditOnlyApplicationModules = %#v, want Prose Editor promoted out of it",
			auditOnlyApplicationModules)
	}
	if !contains(applicationModules, module) || contains(subModules, module) {
		t.Fatalf("Prose Editor registry membership is wrong: applications=%#v submodules=%#v",
			applicationModules, subModules)
	}
	foundTest := false
	for _, target := range testTargets() {
		if target.module == module {
			foundTest = true
		}
	}
	if !foundTest {
		t.Fatal("runnable Prose Editor is missing from root test targets")
	}
	foundGate := false
	for _, gate := range releaseGates("..") {
		if gate.dir == filepath.Join("..", filepath.FromSlash(module)) {
			foundGate = reflect.DeepEqual(gate.args, []string{"mage", "integration:all"})
		}
	}
	if !foundGate {
		t.Fatal("runnable Prose Editor is missing its release integration gate")
	}
}

func TestEveryOrchestratedModuleDirectoryExists(t *testing.T) {
	modules := append(append([]string{}, subModules...), applicationModules...)
	modules = append(modules, auditOnlyApplicationModules...)
	for _, module := range modules {
		info, err := os.Stat(filepath.Join("..", filepath.FromSlash(module)))
		if err != nil {
			t.Errorf("orchestrated module %s does not resolve to a directory: %v", module, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("indexed module %s is not a directory", module)
		}
	}
}

func TestOrchestrationUsesStableApplicationPaths(t *testing.T) {
	wantSubModules := []string{
		"agent-core",
		"applications/catalog",
		"design-patterns",
	}
	if !reflect.DeepEqual(subModules, wantSubModules) {
		t.Fatalf("subModules = %#v, want %#v", subModules, wantSubModules)
	}
	wantApplications := []string{
		"applications/chatbot-mesh",
		"applications/coding-agent",
		"applications/agent-architecture",
		"applications/prose-editor",
	}
	if !reflect.DeepEqual(applicationModules, wantApplications) {
		t.Fatalf("applicationModules = %#v, want %#v", applicationModules, wantApplications)
	}
	wantAuditOnly := []string{
		"applications/large-context-swarm",
	}
	if !reflect.DeepEqual(auditOnlyApplicationModules, wantAuditOnly) {
		t.Fatalf("auditOnlyApplicationModules = %#v, want %#v",
			auditOnlyApplicationModules, wantAuditOnly)
	}
}

// TestLargeContextSwarmIsAuditOnly pins the swarm module's registry membership
// while it ships documents and a manifest but no profiles: it participates in
// the audit and stats gates and stays out of the runnable, build, and Go-test
// registries until release 01.0 supplies executable evidence.
func TestLargeContextSwarmIsAuditOnly(t *testing.T) {
	const module = "applications/large-context-swarm"
	if !contains(auditOnlyApplicationModules, module) {
		t.Fatalf("auditOnlyApplicationModules = %#v, want it to include %s",
			auditOnlyApplicationModules, module)
	}
	if contains(applicationModules, module) || contains(subModules, module) {
		t.Fatalf("swarm registry membership is wrong: applications=%#v submodules=%#v",
			applicationModules, subModules)
	}
	if !contains(auditParticipants(), module) {
		t.Fatalf("auditParticipants() = %#v, want %s", auditParticipants(), module)
	}
	if !contains(statsParticipants(), module) {
		t.Fatalf("statsParticipants() = %#v, want %s", statsParticipants(), module)
	}
}

// TestApplicationModulesExcludedFromSubModules proves runnable application modules
// do not enter
// the Build and All gates, which iterate subModules and would fail on a module
// that defines no build/default target.
func TestApplicationModulesExcludedFromSubModules(t *testing.T) {
	modules := append(append([]string{}, applicationModules...), auditOnlyApplicationModules...)
	for _, mod := range modules {
		if contains(subModules, mod) {
			t.Fatalf("subModules must not contain application module %q (it has no build target)", mod)
		}
	}
}

// TestStatsParticipantsIncludeApplicationModules proves the root stats gate dispatches
// to the application modules, so the repo-wide agents total cannot silently drop
// application agents (GH-754).
func TestStatsParticipantsIncludeApplicationModules(t *testing.T) {
	participants := statsParticipants()
	for _, mod := range applicationModules {
		if !contains(participants, mod) {
			t.Fatalf("statsParticipants() = %#v, missing application module %q", participants, mod)
		}
	}
	for _, mod := range subModules {
		if !contains(participants, mod) {
			t.Fatalf("statsParticipants() = %#v, missing sub-module %q", participants, mod)
		}
	}
	for _, mod := range auditOnlyApplicationModules {
		if !contains(participants, mod) {
			t.Fatalf("statsParticipants() = %#v, missing audit-only module %q", participants, mod)
		}
	}
}

// TestTestSubModulesDispatchesApplicationModules proves the go-test dispatch path
// visits every application module that owns Go tests.
func TestTestSubModulesDispatchesApplicationModules(t *testing.T) {
	var got []string
	err := testSubModules(
		applicationModules,
		func(string) (bool, error) { return true, nil },
		func(dir string) error {
			got = append(got, dir)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("testSubModules returned error: %v", err)
	}
	if !reflect.DeepEqual(got, applicationModules) {
		t.Fatalf("tested application modules = %#v, want %#v", got, applicationModules)
	}
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
