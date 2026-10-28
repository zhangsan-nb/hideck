package mcpserver

import (
	"encoding/json"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type outputContract struct {
	schema   *jsonschema.Schema
	resolved *jsonschema.Resolved
}

func validateSerializedOutput(result *mcp.CallToolResult, schema *jsonschema.Resolved) error {
	// Validate the actual wire JSON, including custom marshalers and nullable fields.
	var value any
	if err := json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &value); err != nil {
		return err
	}
	return schema.Validate(value)
}

func resolveOutputSchema(tool Tool) (outputContract, error) {
	if len(tool.OutputSchema) == 0 {
		return outputContract{}, nil
	}
	var declared jsonschema.Schema
	if err := json.Unmarshal(tool.OutputSchema, &declared); err != nil {
		return outputContract{}, fmt.Errorf("%s output schema: %w", tool.Name, err)
	}
	// Keep local JSON pointers rooted at the declared schema when composing errors.
	if declared.ID == "" {
		declared.ID = "urn:hideck:output:" + tool.Name
	}
	// Tool errors keep their existing shape, including parameter and cancellation errors.
	schema := &jsonschema.Schema{Type: "object", AnyOf: []*jsonschema.Schema{
		&declared,
		{Type: "object", Properties: map[string]*jsonschema.Schema{"error": {Type: "string"}}, Required: []string{"error"}},
	}}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		return outputContract{}, fmt.Errorf("%s output schema: %w", tool.Name, err)
	}
	return outputContract{schema: schema, resolved: resolved}, nil
}
