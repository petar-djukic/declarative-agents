// Copyright (c) 2026 Nokia. All rights reserved.

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Nokia-Bell-Labs/declarative-agents/magefiles/appmanifest"
	"gopkg.in/yaml.v3"
)

const (
	applicationName     = "large-context-swarm"
	applicationManifest = "agents/application.yaml"
)

// requiredREADMESections are the headings srd003-application-consistency R6.1
// requires of every application README.
var requiredREADMESections = []string{
	"Purpose",
	"Status",
	"Composition",
	"Capabilities",
	"Ownership Boundaries",
	"Run or Planned Entry Points",
	"Verification",
	"Documentation",
}

// plannedRoots are the composition roots the manifest reserves. They carry
// planned: true until the profiles ship, and the audit rejects a manifest that
// claims either of them exists before its profile does.
var plannedRoots = []string{"rlm-root", "rlm-worker"}

// Audit validates the manifest, parses every local document, and checks that no
// document claims capability the module does not have.
func Audit() error {
	root, err := applicationRootFromWorkingDirectory()
	if err != nil {
		return err
	}
	manifest, err := loadApplicationManifest(root)
	if err != nil {
		return err
	}
	if err := validateSwarmManifest(root, manifest); err != nil {
		return err
	}
	count, err := auditYAMLDocuments(root)
	if err != nil {
		return err
	}
	if err := auditREADME(root); err != nil {
		return err
	}
	if err := auditStatusClaims(root, manifest); err != nil {
		return err
	}
	fmt.Printf("audit: validated %s manifest and %d YAML documents\n", applicationName, count)
	return nil
}

// Stats reports the module's composition. Planned roots contribute no agents,
// so the repository-wide agent total is unchanged until the profiles ship.
func Stats() error {
	root, err := applicationRootFromWorkingDirectory()
	if err != nil {
		return err
	}
	manifest, err := loadApplicationManifest(root)
	if err != nil {
		return err
	}
	stats, err := newStats(root, manifest)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(stats)
	if err != nil {
		return fmt.Errorf("encode %s stats: %w", applicationName, err)
	}
	fmt.Println(string(encoded))
	return nil
}

type applicationStats struct {
	Application struct {
		Ownership           string   `json:"ownership"`
		ModuleStatus        string   `json:"module_status"`
		AgentsContributed   int      `json:"agents_contributed"`
		CanonicalReferences int      `json:"canonical_references"`
		CompositionWrappers int      `json:"composition_wrappers"`
		PlannedRoots        []string `json:"planned_roots"`
		ExecutableRoots     []string `json:"executable_roots"`
		Documents           int      `json:"documents"`
	} `json:"application"`
}

func newStats(root string, manifest appmanifest.Manifest) (applicationStats, error) {
	var result applicationStats
	result.Application.Ownership = manifest.Ownership
	result.Application.ModuleStatus = manifest.ModuleStatus
	for _, entry := range manifest.Roots {
		switch {
		case entry.Ownership == "catalog":
			result.Application.CanonicalReferences++
		case entry.Planned:
			result.Application.PlannedRoots = append(result.Application.PlannedRoots, entry.ID)
		default:
			result.Application.ExecutableRoots = append(result.Application.ExecutableRoots, entry.ID)
			result.Application.AgentsContributed++
		}
	}
	sort.Strings(result.Application.PlannedRoots)
	sort.Strings(result.Application.ExecutableRoots)
	documents, err := documentPaths(root)
	if err != nil {
		return result, err
	}
	result.Application.Documents = len(documents)
	return result, nil
}

func loadApplicationManifest(root string) (appmanifest.Manifest, error) {
	return appmanifest.Load(
		filepath.Join(root, filepath.FromSlash(applicationManifest)),
		appmanifest.Options{ApplicationRoot: root},
	)
}

// validateSwarmManifest checks identity, the audit-only claim, and that every
// root marked planned has no profile on disk while every root not marked
// planned does. A manifest that keeps planned: true after its profile ships is
// as wrong as one that drops it before.
func validateSwarmManifest(root string, manifest appmanifest.Manifest) error {
	if manifest.Application != applicationName || manifest.Ownership != "agent-owning" {
		return fmt.Errorf("manifest identity = %q/%q, want %q/agent-owning",
			manifest.Application, manifest.Ownership, applicationName)
	}
	if len(manifest.Deployment.Entries) != 0 || len(manifest.UI.Assets) != 0 ||
		len(manifest.Package.Assets) != 0 || manifest.Runtime.MountPath != "" {
		return errors.New("audit-only swarm manifest must not declare deployment, UI, package, or runtime mount surface")
	}

	var declared []string
	for _, entry := range manifest.Roots {
		if entry.Ownership != "local" {
			return fmt.Errorf("root %s is %s; the swarm declares no catalog root until its wrappers ship",
				entry.ID, entry.Ownership)
		}
		declared = append(declared, entry.ID)
		profile := filepath.Join(root, filepath.FromSlash(entry.Source))
		_, err := os.Stat(profile)
		switch {
		case entry.Planned && err == nil:
			return fmt.Errorf("root %s is marked planned but %s exists", entry.ID, entry.Source)
		case !entry.Planned && err != nil:
			return fmt.Errorf("root %s is not marked planned but %s is absent", entry.ID, entry.Source)
		}
	}
	sort.Strings(declared)
	if strings.Join(declared, "\x00") != strings.Join(plannedRoots, "\x00") {
		return fmt.Errorf("declared roots = %v, want %v", declared, plannedRoots)
	}
	return nil
}

// auditStatusClaims fails when a document claims more than the manifest does.
// The module is audit-only, so no document may report an implemented release,
// use case, or suite.
func auditStatusClaims(root string, manifest appmanifest.Manifest) error {
	var architecture struct {
		CapabilityClassification struct {
			Ownership         string `yaml:"ownership"`
			ModuleStatus      string `yaml:"module_status"`
			AgentsContributed int    `yaml:"agents_contributed"`
		} `yaml:"capability_classification"`
	}
	if err := readYAML(filepath.Join(root, "docs", "ARCHITECTURE.yaml"), &architecture); err != nil {
		return err
	}
	if architecture.CapabilityClassification.Ownership != manifest.Ownership {
		return fmt.Errorf("architecture ownership = %q, manifest says %q",
			architecture.CapabilityClassification.Ownership, manifest.Ownership)
	}
	if architecture.CapabilityClassification.ModuleStatus != manifest.ModuleStatus {
		return fmt.Errorf("architecture module_status = %q, manifest says %q",
			architecture.CapabilityClassification.ModuleStatus, manifest.ModuleStatus)
	}
	if architecture.CapabilityClassification.AgentsContributed != 0 {
		return fmt.Errorf("architecture claims %d contributed agents; the module ships none",
			architecture.CapabilityClassification.AgentsContributed)
	}

	var roadmap struct {
		Releases []struct {
			Version string `yaml:"version"`
			Status  string `yaml:"status"`
		} `yaml:"releases"`
	}
	if err := readYAML(filepath.Join(root, "docs", "road-map.yaml"), &roadmap); err != nil {
		return err
	}
	if len(roadmap.Releases) == 0 {
		return errors.New("road-map declares no releases")
	}
	for _, release := range roadmap.Releases {
		if release.Status == "implemented" || release.Status == "done" {
			return fmt.Errorf("road-map release %s claims %s with no executable evidence",
				release.Version, release.Status)
		}
	}
	return nil
}

func auditREADME(root string) error {
	data, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		return fmt.Errorf("read README.md: %w", err)
	}
	content := string(data)
	for _, section := range requiredREADMESections {
		if !strings.Contains(content, "\n## "+section+"\n") {
			return fmt.Errorf("README.md missing required section %q", section)
		}
	}
	return nil
}

func auditYAMLDocuments(root string) (int, error) {
	files, err := documentPaths(root)
	if err != nil {
		return 0, err
	}
	for _, path := range files {
		if err := parseSingleYAML(path); err != nil {
			return 0, err
		}
	}
	return len(files), nil
}

func documentPaths(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(filepath.Join(root, "docs"), func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && (strings.HasSuffix(entry.Name(), ".yaml") || strings.HasSuffix(entry.Name(), ".yml")) {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s documents: %w", applicationName, err)
	}
	files = append(files, filepath.Join(root, filepath.FromSlash(applicationManifest)))
	sort.Strings(files)
	return files, nil
}

func parseSingleYAML(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", filepath.ToSlash(path), err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var document yaml.Node
	if err := decoder.Decode(&document); err == io.EOF {
		return fmt.Errorf("parse %s: empty YAML document", filepath.ToSlash(path))
	} else if err != nil {
		return fmt.Errorf("parse %s: %w", filepath.ToSlash(path), err)
	}
	if len(document.Content) == 0 {
		return fmt.Errorf("parse %s: empty YAML document", filepath.ToSlash(path))
	}
	var trailing yaml.Node
	if err := decoder.Decode(&trailing); err == nil {
		return fmt.Errorf("parse %s: expected exactly one YAML document", filepath.ToSlash(path))
	} else if err != io.EOF {
		return fmt.Errorf("parse %s: %w", filepath.ToSlash(path), err)
	}
	return nil
}

func applicationRootFromWorkingDirectory() (string, error) {
	working, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}
	if filepath.Base(working) == "magefiles" {
		working = filepath.Dir(working)
	}
	if filepath.Base(working) != applicationName {
		return "", fmt.Errorf("run this target from applications/%s", applicationName)
	}
	return working, nil
}

func readYAML(path string, out any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", filepath.ToSlash(path), err)
	}
	if err := yaml.Unmarshal(data, out); err != nil {
		return fmt.Errorf("parse %s: %w", filepath.ToSlash(path), err)
	}
	return nil
}
