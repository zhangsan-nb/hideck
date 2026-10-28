package config

import yaml "go.yaml.in/yaml/v3"

func UpdateSystemInFile(path string, system SystemConfig) error {
	return updateConfigInFile(path, func(root *yaml.Node) error {
		systemNode := getMapValue(root, "system")
		if systemNode == nil || systemNode.Kind != yaml.MappingNode {
			systemNode = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			setMapNode(root, "system", systemNode)
		}
		setMapBool(systemNode, "openwrt_dynamic_interfaces", system.OpenWRTDynamicInterfaces)
		return nil
	})
}

func UpdateAutoAnswerDeviceInFile(path, deviceID string, config AutoAnswerDeviceConfig) error {
	return updateConfigInFile(path, func(root *yaml.Node) error {
		autoAnswerNode := ensureAutoAnswerNode(root)
		devicesNode := getMapValue(autoAnswerNode, "devices")
		if devicesNode == nil || devicesNode.Kind != yaml.MappingNode {
			devicesNode = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			setMapNode(autoAnswerNode, "devices", devicesNode)
		}
		deviceNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		setMapBool(deviceNode, "enabled", config.Enabled)
		setMapInt(deviceNode, "delay_seconds", config.DelaySeconds)
		setMapNode(devicesNode, deviceID, deviceNode)
		return nil
	})
}

func UpdateAutoAnswerAudioFileInFile(path, audioFile string) error {
	return updateConfigInFile(path, func(root *yaml.Node) error {
		setMapScalar(ensureAutoAnswerNode(root), "audio_file", audioFile)
		return nil
	})
}

func ensureAutoAnswerNode(root *yaml.Node) *yaml.Node {
	systemNode := getMapValue(root, "system")
	if systemNode == nil || systemNode.Kind != yaml.MappingNode {
		systemNode = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		setMapNode(root, "system", systemNode)
	}
	autoAnswerNode := getMapValue(systemNode, "auto_answer")
	if autoAnswerNode == nil || autoAnswerNode.Kind != yaml.MappingNode {
		autoAnswerNode = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		setMapNode(systemNode, "auto_answer", autoAnswerNode)
	}
	return autoAnswerNode
}
