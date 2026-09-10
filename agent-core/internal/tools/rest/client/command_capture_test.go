// Copyright (c) 2026 Nokia
// SPDX-License-Identifier: BSD-3-Clause

package client

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Nokia-Bell-Labs/declarative-agents/agent-core/internal/observability/tracing"
)

// Request-body capture (GH-93): at telemetry capture full, the dispatch span
// records the rendered request body — the prompt, when the operation invokes
// an LLM over REST — truncated so an embedding batch cannot bloat the span.

func requestWithBody(t *testing.T, body string) *http.Request {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, "https://api.cohere.com/v2/chat", strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(body)), nil
	}
	return request
}

func capturedAttr(tracer *tracing.RecordingTracer, key string) (string, bool) {
	if len(tracer.Spans) == 0 {
		return "", false
	}
	value, ok := tracer.Spans[0].SetAttrs[key]
	if !ok {
		return "", false
	}
	return fmt.Sprintf("%v", value), true
}

// captureTracer returns a recorder plus the child tracer a dispatch would
// inject, since SetAttributes lands on the active span, not the root.
func captureTracer() (*tracing.RecordingTracer, tracing.Tracer) {
	root := tracing.NewRecordingTracer()
	child, _ := root.Push("execute_tool test")
	return root, child
}

func TestCaptureOnRecordsTheRequestBody(t *testing.T) {
	t.Parallel()
	root, child := captureTracer()
	cmd := &clientCmd{toolName: "invoke_cohere_command", captureContent: true}
	cmd.SetTracer(child)
	cmd.recordRequestCapture(requestWithBody(t, `{"messages":[{"role":"user","content":"the prompt"}]}`))
	body, ok := capturedAttr(root, "http.request.body")
	if !ok || !strings.Contains(body, "the prompt") {
		t.Errorf("http.request.body = %q (present=%v), want the rendered body", body, ok)
	}
	if _, truncated := capturedAttr(root, "http.request.body.truncated"); truncated {
		t.Error("a body under the limit must not be marked truncated")
	}
}

func TestCaptureOffRecordsNothing(t *testing.T) {
	t.Parallel()
	root, child := captureTracer()
	cmd := &clientCmd{toolName: "invoke_cohere_command"}
	cmd.SetTracer(child)
	cmd.recordRequestCapture(requestWithBody(t, `{"messages":[]}`))
	if _, ok := capturedAttr(root, "http.request.body"); ok {
		t.Error("capture off must record no request body")
	}
}

func TestCaptureTruncatesAtTheLimit(t *testing.T) {
	t.Parallel()
	root, child := captureTracer()
	cmd := &clientCmd{toolName: "rag_query", captureContent: true}
	cmd.SetTracer(child)
	large := string(bytes.Repeat([]byte("x"), captureBodyLimit+512))
	cmd.recordRequestCapture(requestWithBody(t, large))
	body, ok := capturedAttr(root, "http.request.body")
	if !ok || len(body) != captureBodyLimit {
		t.Errorf("captured %d bytes (present=%v), want exactly the %d limit", len(body), ok, captureBodyLimit)
	}
	if _, truncated := capturedAttr(root, "http.request.body.truncated"); !truncated {
		t.Error("a body over the limit must carry the truncation marker")
	}
}

// Response-body capture (GH-95): the tee records what the mapping reads, so
// the dispatch span carries the model's answer for a chat operation.

func teeThrough(t *testing.T, capture *responseCapture, body string) {
	t.Helper()
	reader := io.TeeReader(strings.NewReader(body), capture)
	if _, err := io.ReadAll(reader); err != nil {
		t.Fatalf("read through tee: %v", err)
	}
}

func TestResponseCaptureRecordsTheBody(t *testing.T) {
	t.Parallel()
	root, child := captureTracer()
	cmd := &clientCmd{toolName: "invoke_cohere_command", captureContent: true}
	cmd.SetTracer(child)
	capture := &responseCapture{}
	teeThrough(t, capture, `{"message":{"role":"assistant","content":[{"type":"text","text":"the answer"}]}}`)
	cmd.recordResponseCapture(capture)
	body, ok := capturedAttr(root, "http.response.body")
	if !ok || !strings.Contains(body, "the answer") {
		t.Errorf("http.response.body = %q (present=%v), want the read body", body, ok)
	}
	if _, truncated := capturedAttr(root, "http.response.body.truncated"); truncated {
		t.Error("a body under the limit must not be marked truncated")
	}
}

func TestResponseCaptureOffRecordsNothing(t *testing.T) {
	t.Parallel()
	root, child := captureTracer()
	cmd := &clientCmd{toolName: "invoke_cohere_command"}
	cmd.SetTracer(child)
	cmd.recordResponseCapture(nil)
	if _, ok := capturedAttr(root, "http.response.body"); ok {
		t.Error("capture off must record no response body")
	}
}

func TestResponseCaptureTruncatesAtTheLimit(t *testing.T) {
	t.Parallel()
	root, child := captureTracer()
	cmd := &clientCmd{toolName: "query_embedding", captureContent: true}
	cmd.SetTracer(child)
	capture := &responseCapture{}
	teeThrough(t, capture, string(bytes.Repeat([]byte("v"), captureBodyLimit+512)))
	cmd.recordResponseCapture(capture)
	body, ok := capturedAttr(root, "http.response.body")
	if !ok || len(body) != captureBodyLimit {
		t.Errorf("captured %d bytes (present=%v), want exactly the %d limit", len(body), ok, captureBodyLimit)
	}
	if _, truncated := capturedAttr(root, "http.response.body.truncated"); !truncated {
		t.Error("a body over the limit must carry the truncation marker")
	}
}
