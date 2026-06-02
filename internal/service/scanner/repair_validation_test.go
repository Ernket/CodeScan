package scanner

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"codescan/internal/config"

	"github.com/sashabaranov/go-openai"
)

func TestParseValidatedRepairJSONStages(t *testing.T) {
	valid := map[string]string{
		"init":      `[{"method":"GET","path":"/health","source":"api.go","description":"health"}]`,
		"rce":       `[{"type":"RCE","description":"rce"}]`,
		"injection": `[{"type":"Injection","description":"sql"}]`,
		"auth":      `[{"type":"Authentication","affected_endpoints":[]}]`,
		"access":    `[{"type":"Authorization","affected_endpoints":[]}]`,
		"xss":       `[{"type":"XSS","description":"xss"}]`,
		"config":    `[{"type":"Configuration","affected_endpoints":[]}]`,
		"fileop":    `[{"type":"FileOperation","description":"file"}]`,
		"logic":     `[{"type":"BusinessLogic","affected_endpoints":[],"manipulated_fields":[]}]`,
	}

	for _, stage := range SupportedRepairStages() {
		t.Run(stage+"/valid", func(t *testing.T) {
			items, raw, err := ParseValidatedRepairJSON(valid[stage], stage)
			if err != nil {
				t.Fatalf("expected valid stage output: %v", err)
			}
			if len(items) != 1 {
				t.Fatalf("expected one parsed item, got %d", len(items))
			}
			if !json.Valid(raw) {
				t.Fatalf("expected canonical raw JSON, got %s", string(raw))
			}
		})

		t.Run(stage+"/non-json", func(t *testing.T) {
			if _, _, err := ParseValidatedRepairJSON("not json", stage); err == nil {
				t.Fatal("expected non-JSON output to fail")
			}
		})

		t.Run(stage+"/object", func(t *testing.T) {
			if _, _, err := ParseValidatedRepairJSON(`{"type":"RCE"}`, stage); err == nil {
				t.Fatal("expected JSON object output to fail")
			}
		})

		if stage != "init" {
			t.Run(stage+"/wrong-type", func(t *testing.T) {
				if _, _, err := ParseValidatedRepairJSON(`[{"type":"Wrong"}]`, stage); err == nil {
					t.Fatal("expected wrong finding type to fail")
				}
			})
		}

		switch stage {
		case "auth", "access", "config":
			t.Run(stage+"/bad-array-field", func(t *testing.T) {
				payload := fmt.Sprintf(`[{"type":%q,"affected_endpoints":"GET /x"}]`, stageFindingType(stage))
				if _, _, err := ParseValidatedRepairJSON(payload, stage); err == nil {
					t.Fatal("expected string affected_endpoints to fail")
				}
			})
		case "logic":
			t.Run(stage+"/bad-array-field", func(t *testing.T) {
				payload := `[{"type":"BusinessLogic","affected_endpoints":[],"manipulated_fields":"amount"}]`
				if _, _, err := ParseValidatedRepairJSON(payload, stage); err == nil {
					t.Fatal("expected string manipulated_fields to fail")
				}
			})
		}
	}
}

func TestParseValidatedRepairJSONInitRequiresRouteFields(t *testing.T) {
	_, _, err := ParseValidatedRepairJSON(`[{"method":"GET","path":"/health","source":"api.go"}]`, "init")
	if err == nil {
		t.Fatal("expected missing route description to fail")
	}
}

func TestRepairJSONRejectsInvalidRepairedOutput(t *testing.T) {
	useTestAIRequestPolicy(t, 1)
	useTestAIConfig(t, config.AIConfig{Model: "gpt-4o-mini"})

	client := newTestAIClient(testHTTPDoer(func(req *http.Request) (*http.Response, error) {
		return newChatCompletionHTTPResponse(t, req, `{"type":"RCE"}`), nil
	}))
	useTestAIClientFactory(t, func() *openai.Client {
		return client
	})

	if _, err := RepairJSON("raw text [{bad]", "rce"); err == nil {
		t.Fatal("expected invalid repaired output to fail validation")
	}
}

func TestRepairJSONDoesNotRequestResponseFormat(t *testing.T) {
	useTestAIRequestPolicy(t, 1)
	useTestAIConfig(t, config.AIConfig{Model: "gpt-4o-mini"})

	client := newTestAIClient(testHTTPDoer(func(req *http.Request) (*http.Response, error) {
		body := readRequestBody(t, req)
		if strings.Contains(strings.ToLower(body), "response_format") {
			t.Fatalf("repair request must not set response_format: %s", body)
		}
		return newChatCompletionHTTPResponse(t, req, `[{"method":"GET","path":"/health","source":"api.go","description":"health"}]`), nil
	}))
	useTestAIClientFactory(t, func() *openai.Client {
		return client
	})

	if _, err := RepairJSON("raw text [{bad]", "init"); err != nil {
		t.Fatalf("repair json: %v", err)
	}
}
