// Copyright (c) 2026 Nokia. All rights reserved.

package conformance

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// blackboardFixture is a deterministic Chroma and Ollama protocol double. It
// records what the shipped words actually sent: the collection names they
// create-or-got, and the add body per resolved collection id.
type blackboardFixture struct {
	server *httptest.Server

	mu          sync.Mutex
	collections []string
	adds        map[string][]map[string]any
}

func newBlackboardFixture(t *testing.T) *blackboardFixture {
	t.Helper()
	fixture := &blackboardFixture{adds: map[string][]map[string]any{}}
	fixture.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/embeddings":
			_, _ = w.Write([]byte(`{"embedding":[0.25,0.75]}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/collections"):
			body := decodeBlackboardBody(t, r)
			name, _ := body["name"].(string)
			id := fixture.recordCollection(name)
			_, _ = w.Write([]byte(`{"id":"` + id + `","name":"` + name + `"}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/add"):
			parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
			collectionID := parts[len(parts)-2]
			fixture.recordAdd(collectionID, decodeBlackboardBody(t, r))
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fixture.server.Close)
	return fixture
}

func (f *blackboardFixture) recordCollection(name string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for index, existing := range f.collections {
		if existing == name {
			return fmt.Sprintf("collection-%d", index+1)
		}
	}
	f.collections = append(f.collections, name)
	return fmt.Sprintf("collection-%d", len(f.collections))
}

func (f *blackboardFixture) recordAdd(collectionID string, body map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.adds[collectionID] = append(f.adds[collectionID], body)
}

func (f *blackboardFixture) addsFor(collectionID string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.adds[collectionID]...)
}

func (f *blackboardFixture) collectionNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.collections...)
}

func decodeBlackboardBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	body := map[string]any{}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Errorf("decode %s request body: %v", r.URL.Path, err)
	}
	return body
}

// writeMemoryEntry runs the shipped memory-write block against the fixture with
// one request entry and returns the run result.
func writeMemoryEntry(t *testing.T, fixture *blackboardFixture, entry map[string]any) RunResult {
	t.Helper()
	profile := CopyShippedProfile(t,
		filepath.Join("agents", "knowledge-manager", "memory-write", "profile.yaml"),
		map[string]string{"../corpus-rest.yaml": "corpus-rest.yaml"})

	restData, err := os.ReadFile(ProfilePath(filepath.Join("agents", "knowledge-manager", "corpus-rest.yaml")))
	if err != nil {
		t.Fatalf("read shipped corpus REST definition: %v", err)
	}
	parsed, err := url.Parse(fixture.server.URL)
	if err != nil {
		t.Fatalf("parse fixture URL: %v", err)
	}
	rest := strings.ReplaceAll(string(restData), "http://127.0.0.1:11434", fixture.server.URL)
	rest = strings.ReplaceAll(rest, "http://127.0.0.1:8000", fixture.server.URL)
	rest = strings.ReplaceAll(rest, "ports: [8000, 11434]", "ports: ["+parsed.Port()+"]")
	if err := os.WriteFile(filepath.Join(filepath.Dir(profile), "corpus-rest.yaml"), []byte(rest), 0o644); err != nil {
		t.Fatalf("write patched corpus REST definition: %v", err)
	}

	request, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("encode request entry: %v", err)
	}
	requestPath := filepath.Join(t.TempDir(), "entry.json")
	if err := os.WriteFile(requestPath, request, 0o644); err != nil {
		t.Fatalf("write request file: %v", err)
	}

	return Run(t, RunConfig{Profile: profile, Directory: t.TempDir(), Request: requestPath})
}

func blackboardEntry(collection, id, content string, round int) map[string]any {
	return map[string]any{
		"content":    content,
		"id":         id,
		"collection": collection,
		"source":     "derived",
		"agent":      "rlm-worker",
		"round":      round,
	}
}

// TestBlackboardMemoryWriteBlock executes the shipped memory-write block
// against deterministic Chroma and Ollama protocol fixtures. Only transport
// addresses are patched; the shipped machine, declarations, REST operations,
// and tool selection stay in control of sequencing and threading.
//
// Traces srd023-blackboard-memory R4: the entry arrives as request input, the
// machine sequences embed, resolve, and write, and the run returns the record
// id rather than the stored content.
func TestBlackboardMemoryWriteBlock(t *testing.T) {
	t.Parallel()
	fixture := newBlackboardFixture(t)
	const content = "The reducer merged four worker findings into one claim."
	result := writeMemoryEntry(t, fixture, blackboardEntry("task-alpha", "finding-0042", content, 3))

	// R4.2: the machine reaches its success terminal through the three words.
	result.RequireExit(t, 0)
	result.RootRequired(t)
	result.RequireNoErrorSpans(t)
	result.RequireToolSpans(t, "embed_memory", "resolve_memory_collection", "write_memory")
	result.RequireTerminalState(t, "Succeeded")

	// R4.3: the block writes what it is given; it selects no discovery or read
	// word that could introduce content the caller did not supply.
	var selection struct {
		Tools []string `yaml:"tools"`
	}
	readKnowledgeYAML(t,
		filepath.Join("..", "agents", "knowledge-manager", "memory-write", "tools.yaml"),
		&selection)
	for _, tool := range selection.Tools {
		if tool == "read_resource" || tool == "list_resource" || tool == "invoke_llm" {
			t.Errorf("memory-write selects %q; the block writes only the supplied entry", tool)
		}
	}

	// R1.1, R4.3: the collection the request named is the collection created.
	if got := fixture.collectionNames(); len(got) != 1 || got[0] != "task-alpha" {
		t.Fatalf("create-or-got collections = %v, want [task-alpha]", got)
	}

	// R2.1, R2.2: the stored record carries its provenance.
	adds := fixture.addsFor("collection-1")
	if len(adds) != 1 {
		t.Fatalf("adds against the named collection = %d, want 1", len(adds))
	}
	metadatas, ok := adds[0]["metadatas"].([]any)
	if !ok || len(metadatas) != 1 {
		t.Fatalf("add body metadatas = %#v, want one entry", adds[0]["metadatas"])
	}
	metadata, _ := metadatas[0].(map[string]any)
	for field, want := range map[string]any{"source": "derived", "agent": "rlm-worker", "round": float64(3)} {
		if got := metadata[field]; got != want {
			t.Errorf("stored metadata %s = %#v, want %#v", field, got, want)
		}
	}

	// The document and the provider vector reach Chroma as sent, so the entry is
	// retrievable by content as well as by tag.
	if documents, _ := adds[0]["documents"].([]any); len(documents) != 1 || documents[0] != content {
		t.Errorf("stored documents = %#v, want the supplied content", adds[0]["documents"])
	}
	if embeddings, _ := adds[0]["embeddings"].([]any); len(embeddings) != 1 {
		t.Errorf("stored embeddings = %#v, want the threaded provider vector", adds[0]["embeddings"])
	}

	// R4.4, D2: the run hands back a handle, not the content it stored.
	if !strings.Contains(result.Output, "finding-0042") {
		t.Errorf("run output does not carry the record id:\n%s", result.Output)
	}
	if strings.Contains(result.Output, content) {
		t.Errorf("run output echoes the stored content; a write returns a handle:\n%s", result.Output)
	}
}

// TestBlackboardMemoryCollectionIsolation pins srd023 R1.1: two runs naming two
// collections write to two collections, so per-task blackboards do not share
// entries.
func TestBlackboardMemoryCollectionIsolation(t *testing.T) {
	t.Parallel()
	fixture := newBlackboardFixture(t)
	first := writeMemoryEntry(t, fixture, blackboardEntry("task-alpha", "alpha-1", "Alpha finding.", 1))
	second := writeMemoryEntry(t, fixture, blackboardEntry("task-beta", "beta-1", "Beta finding.", 1))
	first.RequireTerminalState(t, "Succeeded")
	second.RequireTerminalState(t, "Succeeded")

	if got := fixture.collectionNames(); len(got) != 2 || got[0] != "task-alpha" || got[1] != "task-beta" {
		t.Fatalf("create-or-got collections = %v, want [task-alpha task-beta]", got)
	}
	for collectionID, wantID := range map[string]string{"collection-1": "alpha-1", "collection-2": "beta-1"} {
		adds := fixture.addsFor(collectionID)
		if len(adds) != 1 {
			t.Fatalf("adds against %s = %d, want 1", collectionID, len(adds))
		}
		ids, _ := adds[0]["ids"].([]any)
		if len(ids) != 1 || ids[0] != wantID {
			t.Errorf("%s stored ids = %#v, want [%s]", collectionID, adds[0]["ids"], wantID)
		}
	}
}
