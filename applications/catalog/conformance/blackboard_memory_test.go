// Copyright (c) 2026 Nokia. All rights reserved.

package conformance

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// The blackboard operations extend the Chroma corpus vocabulary so one
// collection can serve as shared short-term memory: the collection name is a
// declared parameter, writes carry provenance, and reads filter on it
// (srd023-blackboard-memory R1, R2, R3).
//
// They are additive siblings of the release 08.0 operations rather than
// optional parameters on them (srd023 D1). A body value that is a single
// "{{ params.NAME }}" token renders the param's raw typed value, and an
// unsupplied param renders as null rather than being omitted, so an optional
// n_results would reach Chroma as null. Declaring the filter parameters
// required on a separate operation is what keeps the release 08.0 request
// bodies unchanged, which TestBlackboardMemoryCompatibility pins.

type blackboardBinding struct {
	Path         map[string]map[string]any `yaml:"path"`
	BodySchema   blackboardBodySchema      `yaml:"body_schema"`
	BodySource   string                    `yaml:"body_source"`
	InputMapping map[string]string         `yaml:"input_mapping"`
	CarryForward []string                  `yaml:"carry_forward"`
}

type blackboardBodySchema struct {
	Type       string                    `yaml:"type"`
	Required   []string                  `yaml:"required"`
	Properties map[string]map[string]any `yaml:"properties"`
}

type blackboardOperation struct {
	Method  string            `yaml:"method"`
	Path    string            `yaml:"path"`
	Params  blackboardBinding `yaml:"params"`
	Body    map[string]any    `yaml:"body"`
	Success struct {
		Status []int  `yaml:"status"`
		Signal string `yaml:"signal"`
	} `yaml:"success"`
	Response struct {
		Output map[string]string `yaml:"output"`
	} `yaml:"response"`
}

type blackboardREST struct {
	Rest struct {
		Clients map[string]struct {
			Operations map[string]blackboardOperation `yaml:"operations"`
		} `yaml:"clients"`
	} `yaml:"rest"`
}

func readCorpusREST(t *testing.T) blackboardREST {
	t.Helper()
	path := filepath.Join("..", "agents", "knowledge-manager", "corpus-rest.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var parsed blackboardREST
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return parsed
}

func blackboardOp(t *testing.T, rest blackboardREST, client, name string) blackboardOperation {
	t.Helper()
	operations := rest.Rest.Clients[client].Operations
	operation, ok := operations[name]
	if !ok {
		t.Fatalf("corpus REST client %s has no operation %s", client, name)
	}
	return operation
}

func requireBlackboardRequired(t *testing.T, operation blackboardOperation, name string, params ...string) {
	t.Helper()
	required := map[string]bool{}
	for _, field := range operation.Params.BodySchema.Required {
		required[field] = true
	}
	for _, param := range params {
		if !required[param] {
			t.Errorf("%s declares %q optional; an unsupplied param renders as null in the request body",
				name, param)
		}
		if _, declared := operation.Params.BodySchema.Properties[param]; !declared {
			t.Errorf("%s does not declare body param %q", name, param)
		}
	}
}

func requireBlackboardMapping(t *testing.T, operation blackboardOperation, name, target, selector string) {
	t.Helper()
	if got := operation.Params.InputMapping[target]; got != selector {
		t.Errorf("%s input_mapping %s = %q, want %q", name, target, got, selector)
	}
}

func requireBlackboardCarries(t *testing.T, operation blackboardOperation, name string, fields ...string) {
	t.Helper()
	carried := map[string]bool{}
	for _, field := range operation.Params.CarryForward {
		carried[field] = true
	}
	for _, field := range fields {
		if !carried[field] {
			t.Errorf("%s does not carry %q forward; single-hop threading cannot reach the next word",
				name, field)
		}
	}
}

// TestBlackboardMemoryCollectionParameter pins srd023 R1: the named
// create-or-get words take the collection name from a declared parameter,
// resolve the returned collection id, and carry their inbound payload forward
// so the following write or query word threads both.
func TestBlackboardMemoryCollectionParameter(t *testing.T) {
	t.Parallel()
	rest := readCorpusREST(t)

	for _, named := range []struct {
		operation string
		carried   []string
	}{
		{"create_or_get_collection_named", []string{"embeddings", "documents", "ids", "source", "agent", "round"}},
		{"create_or_get_collection_named_query", []string{"query_embeddings", "where", "where_document", "n_results"}},
	} {
		operation := blackboardOp(t, rest, "chroma", named.operation)

		// R1.1: the collection name reaches the body as a parameter, not a literal.
		if got := operation.Body["name"]; got != "{{ params.collection_name }}" {
			t.Errorf("%s body name = %v, want the collection_name param token", named.operation, got)
		}
		requireBlackboardRequired(t, operation, named.operation, "collection_name")
		requireBlackboardMapping(t, operation, named.operation, "collection_name", "$.carried.collection")

		// R1.2: the returned collection id stays resolvable for the add and query paths.
		if got := operation.Response.Output["collection_id"]; got != "$.id" {
			t.Errorf("%s does not resolve the returned collection id: %q", named.operation, got)
		}

		// R1.3: single-hop threading means the payload rides through this word.
		requireBlackboardCarries(t, operation, named.operation, named.carried...)
	}

	// The write chain seeds the collection name at the embed word and carries it
	// to the create-or-get word, so a per-task collection needs no second source.
	embed := blackboardOp(t, rest, "ollama", "embed_memory")
	requireBlackboardRequired(t, embed, "embed_memory", "content", "id", "collection", "source", "agent", "round")
	requireBlackboardCarries(t, embed, "embed_memory", "content", "id", "collection", "source", "agent", "round")
	if got := embed.Body["prompt"]; got != "{{ params.content }}" {
		t.Errorf("embed_memory embeds %v, want the supplied content", got)
	}
}

// TestBlackboardMemoryTaggedWrite pins srd023 R2: the write path sends
// per-record provenance, and it returns the record id as a handle rather than
// echoing the stored content (R4.4).
func TestBlackboardMemoryTaggedWrite(t *testing.T) {
	t.Parallel()
	rest := readCorpusREST(t)
	add := blackboardOp(t, rest, "chroma", "add_records_tagged")

	// R2.1: metadata rides with the record.
	metadatas, ok := add.Body["metadatas"].([]any)
	if !ok || len(metadatas) != 1 {
		t.Fatalf("add_records_tagged body metadatas = %#v, want one record's metadata", add.Body["metadatas"])
	}
	metadata, ok := metadatas[0].(map[string]any)
	if !ok {
		t.Fatalf("add_records_tagged metadata entry = %#v, want an object", metadatas[0])
	}

	// R2.2: the provenance vocabulary readers filter on.
	for field, token := range map[string]string{
		"source": "{{ params.source }}",
		"agent":  "{{ params.agent }}",
		"round":  "{{ params.round }}",
	} {
		if got := metadata[field]; got != token {
			t.Errorf("add_records_tagged metadata %s = %v, want %q", field, got, token)
		}
	}

	// R2.3: every metadata value is a declared param threaded from a prior word,
	// so none of them is read from an unvalidated response body.
	requireBlackboardRequired(t, add, "add_records_tagged", "source", "agent", "round")
	if add.Params.BodySource != "previous_result" {
		t.Errorf("add_records_tagged body_source = %q, want previous_result", add.Params.BodySource)
	}
	for target, selector := range map[string]string{
		"collection": "$.mapped.collection_id",
		"source":     "$.carried.source",
		"agent":      "$.carried.agent",
		"round":      "$.carried.round",
	} {
		requireBlackboardMapping(t, add, "add_records_tagged", target, selector)
	}

	// R4.4: the Chroma add response carries no identifier, so the record id
	// reaches the caller through carry_forward and the content does not.
	requireBlackboardCarries(t, add, "add_records_tagged", "ids")
	for _, field := range add.Params.CarryForward {
		if field == "documents" {
			t.Error("add_records_tagged carries the stored document forward; a write returns a handle, not content")
		}
	}
}

// TestBlackboardMemoryFilteredQuery pins srd023 R3: the filtered query takes a
// metadata filter, an exact-substring filter, and the result count as declared
// required parameters, so none of them can render as null.
func TestBlackboardMemoryFilteredQuery(t *testing.T) {
	t.Parallel()
	rest := readCorpusREST(t)
	query := blackboardOp(t, rest, "chroma", "query_records_filtered")

	// R3.1, R3.2, R3.3: each filter and the count reach the body as a parameter.
	for field, token := range map[string]string{
		"where":          "{{ params.where }}",
		"where_document": "{{ params.where_document }}",
		"n_results":      "{{ params.n_results }}",
	} {
		if got := query.Body[field]; got != token {
			t.Errorf("query_records_filtered body %s = %v, want %q", field, got, token)
		}
	}

	// R3.4: required, because an unsupplied declared param renders as null and
	// Chroma rejects a null result count.
	requireBlackboardRequired(t, query, "query_records_filtered", "where", "where_document", "n_results")
	if got := query.Params.BodySchema.Properties["n_results"]["type"]; got != "integer" {
		t.Errorf("query_records_filtered n_results type = %v, want integer", got)
	}
	for _, field := range []string{"where", "where_document"} {
		if got := query.Params.BodySchema.Properties[field]["type"]; got != "object" {
			t.Errorf("query_records_filtered %s type = %v, want object", field, got)
		}
	}

	// The retrieved metadata is what lets a reader tell a derived entry from an
	// ingested document without reading the content back.
	if got := query.Response.Output["metadatas"]; got != "$.metadatas" {
		t.Errorf("query_records_filtered does not return record metadata: %q", got)
	}
}

// TestBlackboardMemoryCompatibility pins srd023 R5: the release 08.0 operations
// send the requests they sent before the extensions existed, and the shipped
// ingest and reader blocks still bind those operations rather than the new ones.
func TestBlackboardMemoryCompatibility(t *testing.T) {
	t.Parallel()
	rest := readCorpusREST(t)

	// R5.1: the three create-or-get variants still name the fixed collection.
	for _, name := range []string{
		"create_or_get_collection",
		"create_or_get_collection_carry",
		"create_or_get_collection_query",
	} {
		if got := blackboardOp(t, rest, "chroma", name).Body["name"]; got != "corpus" {
			t.Errorf("%s body name = %v, want the release 08.0 literal corpus", name, got)
		}
	}

	// R5.1: the legacy add carries no metadata and the legacy query no filters.
	add := blackboardOp(t, rest, "chroma", "add_records")
	if _, tagged := add.Body["metadatas"]; tagged {
		t.Error("add_records now sends metadatas; provenance belongs to add_records_tagged")
	}
	query := blackboardOp(t, rest, "chroma", "query_records")
	if got := query.Body["n_results"]; got != 5 {
		t.Errorf("query_records n_results = %v, want the release 08.0 literal 5", got)
	}
	for _, field := range []string{"where", "where_document"} {
		if _, filtered := query.Body[field]; filtered {
			t.Errorf("query_records now sends %s; filtering belongs to query_records_filtered", field)
		}
	}

	// R5.2: the shipped blocks bind only the release 08.0 operations, so the
	// extensions cannot change what ingest and reader send.
	extensions := map[string]bool{
		"embed_memory":                         true,
		"create_or_get_collection_named":       true,
		"create_or_get_collection_named_query": true,
		"add_records_tagged":                   true,
		"query_records_filtered":               true,
	}
	for _, block := range []string{"corpus-ingest", "corpus-reader"} {
		var declarations struct {
			Tools []struct {
				Name   string `yaml:"name"`
				Config struct {
					Operation string `yaml:"operation"`
				} `yaml:"config"`
			} `yaml:"tools"`
		}
		readKnowledgeYAML(t,
			filepath.Join("..", "agents", "knowledge-manager", block, "declarations.yaml"),
			&declarations)
		for _, tool := range declarations.Tools {
			if extensions[tool.Config.Operation] {
				t.Errorf("%s word %s binds blackboard operation %s; the release 08.0 blocks stay unchanged",
					block, tool.Name, tool.Config.Operation)
			}
		}
	}
}
