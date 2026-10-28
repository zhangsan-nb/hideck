package api

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"github.com/yibaiba/hideck/internal/backend"

	"github.com/google/jsonschema-go/jsonschema"
)

type mcpOperation struct {
	name, method, path           string
	run                          func(businessContext)
	readOnly, destructive, audio bool
}

var mcpPathParameter = regexp.MustCompile(`\{(\w+)\}`)

func mcpInputSchema(spec map[string]any, op mcpOperation) (json.RawMessage, string, error) {
	paths := spec["paths"].(map[string]any)
	pathItem, _ := paths[op.path].(map[string]any)
	operation, _ := pathItem[strings.ToLower(op.method)].(map[string]any)
	description, _ := operation["summary"].(string)
	if description == "" {
		description = strings.TrimPrefix(op.name, "hideck_")
	}
	properties := map[string]any{}
	required := []string{}
	for _, param := range mcpPathParameter.FindAllStringSubmatch(op.path, -1) {
		properties[param[1]] = map[string]any{"type": "string", "minLength": 1, "description": "现有业务标识：" + param[1]}
		required = append(required, param[1])
	}
	parameters, _ := operation["parameters"].([]any)
	for _, raw := range parameters {
		param := raw.(map[string]any)
		if param["in"] != "query" {
			continue
		}
		name := param["name"].(string)
		properties[name] = mcpExpandSchema(spec, param["schema"])
		if param["required"] == true {
			required = append(required, name)
		}
	}
	if body, ok := operation["requestBody"].(map[string]any); ok {
		content, _ := body["content"].(map[string]any)
		media, _ := content["application/json"].(map[string]any)
		if schema, ok := media["schema"]; ok {
			properties["request"] = mcpExpandSchema(spec, schema)
			if body["required"] == true {
				required = append(required, "request")
			}
		}
	}
	if err := mcpSupplementSchema(op, properties); err != nil {
		return nil, "", err
	}
	if op.audio {
		properties["audio_connection_id"] = map[string]any{"type": "string", "minLength": 1, "description": "电话页授权给当前令牌的网页音频连接 ID"}
		required = append(required, "audio_connection_id")
		if body, ok := properties["request"].(map[string]any); ok {
			if props, ok := body["properties"].(map[string]any); ok {
				delete(props, "media_id")
			}
			if fields, ok := body["required"].([]any); ok {
				filtered := []any{}
				for _, field := range fields {
					if field != "media_id" {
						filtered = append(filtered, field)
					}
				}
				body["required"] = filtered
			}
		}
	}
	schema, err := json.Marshal(map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false})
	if !op.readOnly {
		description += "。此操作可能改变设备、网络、SIM、数据或外部通话/短信状态；写操作失败不会自动重试。"
	}
	return schema, description, err
}

func mcpExpandSchema(spec map[string]any, raw any) any {
	switch node := raw.(type) {
	case map[string]any:
		if ref, ok := node["$ref"].(string); ok {
			var value any = spec
			for _, key := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
				value = value.(map[string]any)[key]
			}
			return mcpExpandSchema(spec, value)
		}
		out := map[string]any{}
		for key, value := range node {
			if key == "nullable" || key == "example" || key == "xml" {
				continue
			}
			out[key] = mcpExpandSchema(spec, value)
		}
		return out
	case []any:
		out := make([]any, len(node))
		for i, value := range node {
			out[i] = mcpExpandSchema(spec, value)
		}
		return out
	default:
		return raw
	}
}

func mcpSupplementSchema(op mcpOperation, properties map[string]any) error {
	bodies := map[string]any{
		"hideck_sim_policy_set": cardPolicyPatchRequest{},
		"hideck_contact_save":   phoneContactRequest{}, "hideck_contact_batch_save": phoneContactBatchRequest{},
		"hideck_contact_batch_delete": phoneContactDeleteRequest{}, "hideck_contact_group_rename": phoneContactGroupRequest{},
		"hideck_network_selection_set": backend.SetOperatorSelectionRequest{},
		"hideck_device_ussd_continue":  continueUSSDRequest{}, "hideck_device_ussd_cancel": cancelUSSDRequest{},
	}
	if body, ok := bodies[op.name]; ok {
		schema, err := jsonschema.ForType(reflect.TypeOf(body), nil)
		if err != nil {
			return fmt.Errorf("%s: %w", op.name, err)
		}
		data, err := json.Marshal(schema)
		if err != nil {
			return err
		}
		var object map[string]any
		if err := json.Unmarshal(data, &object); err != nil {
			return err
		}
		removeMCPInferredRequired(object)
		properties["request"] = object
	}
	extras := map[string][]string{
		"hideck_sim_policies": {"iccid"}, "hideck_sim_policy_get": {},
		"hideck_contact_lookup": {"number", "device_id"}, "hideck_contact_list": {"limit", "offset"},
		"hideck_contact_delete": {"number", "device_id"}, "hideck_contact_export": {"format"}, "hideck_contact_group_delete": {"contact_id"},
		"hideck_network_scan": {}, "hideck_network_selection_get": {},
		"hideck_esim_notification_retry": {"aid_hex"}, "hideck_esim_delete": {"aid_hex"},
		"hideck_esim_profiles": {"aid_hex"}, "hideck_esim_notifications": {"aid_hex"},
	}
	for _, key := range extras[op.name] {
		properties[key] = map[string]any{"type": "string"}
	}
	return nil
}

// Required business fields are validated by the same request decoder/use case.
// Inference otherwise marks optional legacy fields as required, including nested
// contact records that intentionally permit names and device IDs to be omitted.
func removeMCPInferredRequired(value any) {
	switch node := value.(type) {
	case map[string]any:
		delete(node, "required")
		for _, child := range node {
			removeMCPInferredRequired(child)
		}
	case []any:
		for _, child := range node {
			removeMCPInferredRequired(child)
		}
	}
}
