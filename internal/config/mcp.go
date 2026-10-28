package config

import yaml "go.yaml.in/yaml/v3"

type MCPConfig struct {
	Enabled bool `mapstructure:"enabled" json:"enabled"`
}

func UpdateMCPInFile(path string, cfg MCPConfig) error {
	return updateConfigInFile(path, func(root *yaml.Node) error {
		node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		setMapBool(node, "enabled", cfg.Enabled)
		setMapNode(root, "mcp", node)
		return nil
	})
}
