package config

import yaml "go.yaml.in/yaml/v3"

func UpdateArchiveInFile(path string, archive ArchiveConfig) error {
	return updateConfigInFile(path, func(root *yaml.Node) error {
		node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		setMapBool(node, "enabled", archive.Enabled)
		setMapScalar(node, "endpoint", archive.Endpoint)
		setMapScalar(node, "username", archive.Username)
		setMapScalar(node, "password", archive.Password)
		setMapScalar(node, "base_path", archive.BasePath)
		setMapInt(node, "interval_hours", archive.IntervalHours)
		setMapInt(node, "retention_days", archive.RetentionDays)
		setMapBool(node, "auto_cleanup", archive.AutoCleanup)
		setMapBool(node, "include_sms", archive.IncludeSMS)
		setMapBool(node, "include_calls", archive.IncludeCalls)
		setMapBool(node, "include_recordings", archive.IncludeRecordings)
		setMapNode(root, "archive", node)
		return nil
	})
}
