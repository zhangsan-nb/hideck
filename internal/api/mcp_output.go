package api

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/yibaiba/hideck/internal/commandcenter"
	"github.com/yibaiba/hideck/internal/db"
	"github.com/yibaiba/hideck/internal/esim"
	"github.com/yibaiba/hideck/internal/mbn"
)

func mcpOutputSchema(spec map[string]any, op mcpOperation) (json.RawMessage, error) {
	paths := spec["paths"].(map[string]any)
	pathItem, _ := paths[op.path].(map[string]any)
	operation, _ := pathItem[strings.ToLower(op.method)].(map[string]any)
	responses, _ := operation["responses"].(map[string]any)
	codes := make([]string, 0, len(responses))
	for code := range responses {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	variants := []any{}
	for _, code := range codes {
		status, err := strconv.Atoi(code)
		if err != nil || status < 200 || status >= 300 {
			continue
		}
		body := mcpResponseOutputBody(spec, responses[code])
		if status == 204 {
			body = map[string]any{"type": "null"}
		}
		if override, err := mcpOutputSupplement(op.name); err != nil {
			return nil, err
		} else if override != nil {
			body = override
		}
		variants = append(variants, mcpResponseEnvelope(map[string]any{"type": "integer", "const": status}, body))
	}
	// Some legacy operations document only status text. Their envelope remains explicit.
	if len(variants) == 0 {
		variants = append(variants, mcpResponseEnvelope(map[string]any{"type": "integer", "minimum": 200, "maximum": 299}, map[string]any{}))
	}
	return mcpOutputVariants(variants)
}

func mcpResponseOutputBody(spec map[string]any, value any) any {
	response, _ := expandMCPResponse(spec, value).(map[string]any)
	content, _ := response["content"].(map[string]any)
	media, _ := content["application/json"].(map[string]any)
	if body := media["schema"]; body != nil {
		return body
	}
	return map[string]any{"description": "现有业务响应；此接口未在 OpenAPI 声明更细的字段约束。"}
}

func mcpResponseEnvelope(status, body any) map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"status_code": status, "data": body,
	}, "required": []string{"status_code", "data"}}
}

func mcpOutputVariants(success []any) (json.RawMessage, error) {
	failures := []any{
		mcpResponseEnvelope(map[string]any{"type": "integer", "minimum": 400}, map[string]any{}),
		mcpResponseEnvelope(map[string]any{"type": "integer"}, map[string]any{"type": "object", "anyOf": []any{
			map[string]any{"required": []string{"error"}},
			map[string]any{"properties": map[string]any{"status": map[string]any{"const": "error"}}, "required": []string{"status"}},
		}}),
	}
	return json.Marshal(map[string]any{"type": "object", "anyOf": append(append([]any{}, success...), failures...)})
}

func mcpOutputSupplement(name string) (any, error) {
	switch name {
	case "hideck_command_execute":
		return schemaForMCPOutput(struct {
			Execution db.CommandExecution `json:"execution"`
		}{})
	case "hideck_command_results":
		return schemaForMCPOutput(struct {
			Events []commandcenter.Event `json:"events"`
		}{})
	case "hideck_mbn_start":
		return schemaForMCPOutput(struct {
			OperationID string        `json:"operation_id"`
			Operation   mbn.Operation `json:"operation"`
		}{})
	case "hideck_mbn_operations":
		return schemaForMCPOutput(struct {
			Items []mbn.Operation `json:"items"`
		}{})
	case "hideck_esim_download":
		return map[string]any{"type": "object", "properties": map[string]any{
			"step": map[string]any{"const": "done"}, "msg": map[string]any{"type": "string"},
			"pct": map[string]any{"type": "number"}, "space_delta": map[string]any{"type": "object"},
			"warning": map[string]any{"type": "string"}, "warning_code": map[string]any{"type": "string"},
		}, "required": []string{"step"}}, nil
	default:
		return nil, nil
	}
}

func schemaForMCPOutput(value any) (any, error) {
	schema, err := jsonschema.ForType(reflect.TypeOf(value), nil)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	return expandMCPResponse(decoded, decoded), nil
}

func mcpSpecialOutput(name string) (json.RawMessage, error) {
	var value any
	switch name {
	case "hideck_esim_decode_activation":
		value = struct {
			Activation esim.DecodedActivation `json:"activation"`
		}{}
	case "hideck_mbn_file_upload":
		value = struct {
			Asset mbn.Asset `json:"asset"`
		}{}
	case "hideck_phone_audio_connections":
		type connection struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Status string `json:"status"`
		}
		value = struct {
			Connections []connection `json:"connections"`
		}{}
	case "hideck_contact_import":
		body, err := schemaForMCPOutput(struct {
			Imported int `json:"imported"`
			Skipped  int `json:"skipped"`
			Parsed   int `json:"parsed"`
		}{})
		if err != nil {
			return nil, err
		}
		return mcpOutputVariants([]any{mcpResponseEnvelope(map[string]any{"const": 200}, body)})
	case "hideck_phone_auto_answer_audio":
		return mcpOutputVariants([]any{mcpResponseEnvelope(map[string]any{"type": "integer", "minimum": 200, "maximum": 299}, map[string]any{"type": "object"})})
	default:
		return nil, fmt.Errorf("%s: 缺少输出契约", name)
	}
	body, err := schemaForMCPOutput(value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(body)
}

// OpenAPI nullable is translated for output without changing existing input validation.
// Recording paths removed by sanitizeMCPData must also be absent from the contract.
func expandMCPResponse(spec map[string]any, value any) any {
	switch node := value.(type) {
	case map[string]any:
		return expandMCPResponseObject(spec, node)
	case []any:
		result := make([]any, len(node))
		for i, child := range node {
			result[i] = expandMCPResponse(spec, child)
		}
		return result
	default:
		return value
	}
}

func expandMCPResponseObject(spec map[string]any, node map[string]any) any {
	if ref, ok := node["$ref"].(string); ok {
		var target any = spec
		for _, key := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
			target = target.(map[string]any)[key]
		}
		return expandMCPResponse(spec, target)
	}
	result := map[string]any{}
	for key, child := range node {
		if key == "nullable" || key == "example" || key == "xml" {
			continue
		}
		result[key] = expandMCPResponse(spec, child)
	}
	sanitizeMCPOutputProperties(result)
	if node["nullable"] == true {
		return map[string]any{"anyOf": []any{result, map[string]any{"type": "null"}}}
	}
	return result
}

func sanitizeMCPOutputProperties(schema map[string]any) {
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		return
	}
	removed := map[string]bool{}
	for _, key := range []string{"audio_path", "source_audio_path", "pcap_path", "recording_path"} {
		if _, exists := properties[key]; exists {
			delete(properties, key)
			removed[key] = true
		}
	}
	if len(removed) == 0 {
		return
	}
	properties["recording_uri"] = map[string]any{"type": "string", "description": "使用 MCP resources/read 读取，不是服务器文件路径。"}
	if required, ok := schema["required"].([]any); ok {
		filtered := []any{}
		for _, name := range required {
			if !removed[name.(string)] {
				filtered = append(filtered, name)
			}
		}
		schema["required"] = filtered
	}
}
