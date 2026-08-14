// Copyright (c) 2026 Petar Djukic. All rights reserved.

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This file guards application ownership the fork holds and upstream does not.
//
// Upstream deleted the Prose Editor application in 545ed616 (Nokia GH-1624),
// stripping the module and its registrations across 78 files. The fork keeps
// the application, so every upstream sync re-applies that deletion and must
// revert it. The registrations upstream stripped cannot police this: the same
// commit that deletes the module also deletes the entries in build.go, lint.go,
// application_consistency_test.go, stats_test.go, tag_test.go, and
// example_modules_test.go, so a merge taking upstream's side removes the
// application and every assertion about it together, and the suite stays green.
//
// A guard therefore has to live where a merge cannot reach it. Upstream has no
// file at this path, so no merge ever touches it, and it survives the deletion
// it exists to detect.
const proseEditorOwnership = "the fork owns applications/prose-editor; " +
	"upstream deleted it in 545ed616 (Nokia GH-1624). If an upstream sync " +
	"re-applied that deletion, restore it with: git revert 545ed616"

// proseEditorRoots are the executable roots the Prose Editor application owns.
var proseEditorRoots = []string{
	"specialist-editor",
	"structure-rag",
	"voice-critic",
	"workflow-orchestrator",
}

// proseEditorRegistrationDocs are the documents that register Prose Editor.
// Each references the application by its slug, its display name, or both.
var proseEditorRegistrationDocs = []string{
	"applications/README.md",
	"applications/docs/ARCHITECTURE.yaml",
	"applications/docs/SPECIFICATIONS.yaml",
	"applications/docs/road-map.yaml",
	"applications/docs/specs/software-requirements/srd003-application-consistency.yaml",
	"applications/docs/specs/use-cases/rel14.0-uc001-application-consistency.yaml",
	"applications/docs/specs/test-suites/test-rel14.0-application-consistency.yaml",
	"agent-core/docs/constitutions/go-style.yaml",
}

func TestForkOwnsProseEditorModule(t *testing.T) {
	applicationRoot := filepath.Join("..", "applications", "prose-editor")
	manifest := filepath.Join(applicationRoot, "agents", "application.yaml")
	if _, err := os.Stat(manifest); err != nil {
		t.Fatalf("%s is missing: %v\n%s", relativeToRepo(manifest), err, proseEditorOwnership)
	}
	for _, root := range proseEditorRoots {
		rootDir := filepath.Join(applicationRoot, "agents", root)
		info, err := os.Stat(rootDir)
		if err != nil {
			t.Errorf("executable root %s is missing: %v\n%s",
				relativeToRepo(rootDir), err, proseEditorOwnership)
			continue
		}
		if !info.IsDir() {
			t.Errorf("executable root %s is not a directory\n%s",
				relativeToRepo(rootDir), proseEditorOwnership)
		}
	}
}

// TestForkOwnsProseEditorRegistrations reads the package-level registries
// directly rather than parsing their source files, so renaming a registry
// breaks compilation here instead of passing silently.
func TestForkOwnsProseEditorRegistrations(t *testing.T) {
	for _, registry := range []struct {
		name    string
		entries []string
		want    string
	}{
		{"applicationModules", applicationModules, "applications/prose-editor"},
		{"lintModuleDirs", lintModuleDirs, "applications/prose-editor"},
		{"release14Applications", release14Applications, "prose-editor"},
	} {
		if !containsString(registry.entries, registry.want) {
			t.Errorf("%s does not contain %q\n%s",
				registry.name, registry.want, proseEditorOwnership)
		}
	}
}

func TestForkOwnsProseEditorRegistrationDocs(t *testing.T) {
	for _, document := range proseEditorRegistrationDocs {
		filename := filepath.Join("..", filepath.FromSlash(document))
		content, err := os.ReadFile(filename)
		if err != nil {
			t.Errorf("read %s: %v\n%s", document, err, proseEditorOwnership)
			continue
		}
		text := string(content)
		if !strings.Contains(text, "prose-editor") && !strings.Contains(text, "Prose Editor") {
			t.Errorf("%s no longer registers Prose Editor\n%s", document, proseEditorOwnership)
		}
	}
}

// TestForkOwnsProseEditorRoleRealizations checks the semantic model separately
// from the other documents: a bare mention would satisfy the substring check,
// but the taxonomy gate needs every executable root realized by profile path.
func TestForkOwnsProseEditorRoleRealizations(t *testing.T) {
	document := "applications/docs/specs/semantic-models/agent-role-realizations.yaml"
	content, err := os.ReadFile(filepath.Join("..", filepath.FromSlash(document)))
	if err != nil {
		t.Fatalf("read %s: %v\n%s", document, err, proseEditorOwnership)
	}
	text := string(content)
	for _, root := range proseEditorRoots {
		profile := "applications/prose-editor/agents/" + root + "/profile.yaml"
		if !strings.Contains(text, profile) {
			t.Errorf("%s does not realize %s\n%s", document, profile, proseEditorOwnership)
		}
	}
}
