package mcpserver

import (
	"encoding/json"
	"testing"
)

func TestOutputCompositionPreservesLocalReferences(t *testing.T) {
	contract, err := resolveOutputSchema(Tool{Name: "references", OutputSchema: json.RawMessage(`{"type":"object","$defs":{"id":{"type":"integer"}},"properties":{"id":{"$ref":"#/$defs/id"}},"required":["id"]}`)})
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{map[string]any{"id": 1}, map[string]any{"error": "cancelled"}} {
		if err := contract.resolved.Validate(value); err != nil {
			t.Fatal(err)
		}
	}
	if err := contract.resolved.Validate(map[string]any{"id": "wrong type"}); err == nil {
		t.Fatal("reference constraints lost")
	}
}
