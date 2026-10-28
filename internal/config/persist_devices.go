package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/go-viper/mapstructure/v2"
	yaml "go.yaml.in/yaml/v3"
)

var configFileMu sync.Mutex

type DeviceListValidator func([]DeviceConfig) error

type DeviceUpdateRequest struct {
	DeviceID string
	Device   DeviceConfig
	Validate DeviceListValidator
}

func AddDeviceInFile(path string, device DeviceConfig) error {
	return AddDeviceInFileValidated(path, device, nil)
}

func AddDeviceInFileValidated(path string, device DeviceConfig, validate DeviceListValidator) error {
	return updateDevicesInFile(path, func(devices *yaml.Node) (*yaml.Node, error) {
		if findDeviceNodeByID(devices, device.ID) != nil {
			return nil, fmt.Errorf("设备已存在: %s", device.ID)
		}
		if err := validateDeviceListNode(devices, validate); err != nil {
			return nil, err
		}
		devices.Content = append(devices.Content, deviceConfigToNode(device))
		return devices, nil
	})
}

func UpdateDeviceInFile(path string, deviceID string, newDevice DeviceConfig) error {
	return UpdateDeviceInFileValidated(path, DeviceUpdateRequest{DeviceID: deviceID, Device: newDevice})
}

func UpdateDeviceInFileValidated(path string, request DeviceUpdateRequest) error {
	return updateDevicesInFile(path, func(devices *yaml.Node) (*yaml.Node, error) {
		n := findDeviceNodeByID(devices, request.DeviceID)
		if n == nil {
			return nil, fmt.Errorf("设备未找到: %s", request.DeviceID)
		}
		if err := validateDeviceListNode(devices, request.Validate); err != nil {
			return nil, err
		}
		applyDeviceConfigUpdate(n, request.Device)
		return devices, nil
	})
}

func applyDeviceConfigUpdate(node *yaml.Node, device DeviceConfig) {
	setMapScalar(node, "id", device.ID)
	setMapScalar(node, "name", device.Name)
	if device.ModemIMEI != "" {
		setMapScalar(node, "modem_imei", device.ModemIMEI)
	} else {
		deleteMapKey(node, "modem_imei")
	}
	setMapScalar(node, "device_backend", device.DeviceBackend)
	setOptionalMapScalar(node, "pcsc_reader_name", device.PCSCReaderName)
	setOptionalMapScalar(node, "pcsc_usb_path", device.PCSCUSBPath)
	setOptionalMapScalar(node, "sim_pin_env", device.SIMPINEnv)
	if device.USBNetMode != nil {
		setMapInt(node, "usbnet_mode", *device.USBNetMode)
	} else {
		deleteMapKey(node, "usbnet_mode")
	}
	if device.QMIUseProxy {
		setMapBool(node, "qmi_use_proxy", true)
	} else {
		deleteMapKey(node, "qmi_use_proxy")
	}
	if device.QMIProxyPath != "" {
		setMapScalar(node, "qmi_proxy_path", device.QMIProxyPath)
	} else {
		deleteMapKey(node, "qmi_proxy_path")
	}
	if device.QMIProxyExecutable != "" {
		setMapScalar(node, "qmi_proxy_executable", device.QMIProxyExecutable)
	} else {
		deleteMapKey(node, "qmi_proxy_executable")
	}
	if device.ProxyPort > 0 {
		setMapInt(node, "proxy_port", device.ProxyPort)
	} else {
		deleteMapKey(node, "proxy_port")
	}
}

func validateDeviceListNode(devices *yaml.Node, validate DeviceListValidator) error {
	if validate == nil {
		return nil
	}
	var raw []map[string]any
	if err := devices.Decode(&raw); err != nil {
		return fmt.Errorf("解析设备配置失败: %w", err)
	}
	var current []DeviceConfig
	if err := mapstructure.Decode(raw, &current); err != nil {
		return fmt.Errorf("转换设备配置失败: %w", err)
	}
	return validate(current)
}

func setOptionalMapScalar(node *yaml.Node, key, value string) {
	if strings.TrimSpace(value) == "" {
		deleteMapKey(node, key)
		return
	}
	setMapScalar(node, key, value)
}

// UpdateDeviceIMEIInFile 仅回填 modem_imei,绝不触碰路径字段;IMEI 为空时跳过(不擦除已有值)。
func UpdateDeviceIMEIInFile(path string, updates map[string]string) error {
	return updateDevicesInFile(path, func(devices *yaml.Node) (*yaml.Node, error) {
		for deviceID, imei := range updates {
			if strings.TrimSpace(imei) == "" {
				continue
			}
			n := findDeviceNodeByID(devices, deviceID)
			if n == nil {
				return nil, fmt.Errorf("设备未找到: %s", deviceID)
			}
			setMapScalar(n, "modem_imei", strings.TrimSpace(imei))
		}
		return devices, nil
	})
}

func DeleteDeviceInFile(path string, deviceID string) error {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return fmt.Errorf("设备未找到: %s", deviceID)
	}
	return updateConfigInFile(path, func(root *yaml.Node) error {
		devices := getMapValue(root, "devices")
		if devices != nil && devices.Kind == yaml.SequenceNode {
			removeDeviceNode(devices, deviceID)
		}
		removeDeviceFromNotificationFilters(root, deviceID)
		removeDeviceFromAutoAnswer(root, deviceID)
		return nil
	})
}

func removeDeviceFromAutoAnswer(root *yaml.Node, deviceID string) {
	systemNode := getMapValue(root, "system")
	autoAnswerNode := getMapValue(systemNode, "auto_answer")
	devicesNode := getMapValue(autoAnswerNode, "devices")
	deleteMapKey(devicesNode, deviceID)
}

func removeDeviceNode(devices *yaml.Node, deviceID string) {
	for index, item := range devices.Content {
		if item != nil && item.Kind == yaml.MappingNode && strings.TrimSpace(getMapScalar(item, "id")) == deviceID {
			devices.Content = append(devices.Content[:index], devices.Content[index+1:]...)
			return
		}
	}
}

func removeDeviceFromNotificationFilters(root *yaml.Node, deviceID string) {
	filters := getMapValue(root, "notification_device_filters")
	if filters == nil || filters.Kind != yaml.MappingNode {
		return
	}
	for index := len(filters.Content) - 2; index >= 0; index -= 2 {
		devices := filters.Content[index+1]
		if devices == nil || devices.Kind != yaml.SequenceNode {
			continue
		}
		kept := make([]*yaml.Node, 0, len(devices.Content))
		removed := false
		for _, entry := range devices.Content {
			if entry != nil && strings.TrimSpace(entry.Value) == deviceID {
				removed = true
				continue
			}
			kept = append(kept, entry)
		}
		if !removed || len(kept) == 0 {
			// A missing filter means "all devices". Keep the final, now inactive
			// device ID so deleting a device cannot broaden notification access.
			continue
		}
		devices.Content = kept
	}
}

func updateDevicesInFile(path string, mutate func(*yaml.Node) (*yaml.Node, error)) error {
	return updateConfigInFile(path, func(root *yaml.Node) error {
		devices := getMapValue(root, "devices")
		if devices == nil || devices.Kind != yaml.SequenceNode {
			devices = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
			setMapNode(root, "devices", devices)
		}
		_, err := mutate(devices)
		return err
	})
}

func updateConfigInFile(path string, mutate func(*yaml.Node) error) error {
	configFileMu.Lock()
	defer configFileMu.Unlock()

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读取配置文件失败: %w", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("解析配置文件失败: %w", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("配置文件结构错误")
	}
	if err := mutate(doc.Content[0]); err != nil {
		return err
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return fmt.Errorf("序列化配置文件失败: %w", err)
	}
	if err := writeConfigAtomically(path, out); err != nil {
		return err
	}
	if GetConfigPath() == path {
		return ReloadFromFile()
	}
	return nil
}

func writeConfigAtomically(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建配置目录失败: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("写入临时配置文件失败: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("替换配置文件失败: %w", err)
	}
	return nil
}

func findDeviceNodeByID(devices *yaml.Node, id string) *yaml.Node {
	for _, item := range devices.Content {
		if item == nil || item.Kind != yaml.MappingNode {
			continue
		}
		if strings.TrimSpace(getMapScalar(item, "id")) == strings.TrimSpace(id) {
			return item
		}
	}
	return nil
}

func deviceConfigToNode(d DeviceConfig) *yaml.Node {
	m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	appendMapScalar(m, "id", d.ID)
	if d.Name != "" {
		appendMapScalar(m, "name", d.Name)
	}
	if d.ModemIMEI != "" {
		appendMapScalar(m, "modem_imei", d.ModemIMEI)
	}
	if d.DeviceBackend != "" {
		appendMapScalar(m, "device_backend", d.DeviceBackend)
	}
	if d.PCSCReaderName != "" {
		appendMapScalar(m, "pcsc_reader_name", d.PCSCReaderName)
	}
	if d.PCSCUSBPath != "" {
		appendMapScalar(m, "pcsc_usb_path", d.PCSCUSBPath)
	}
	if d.SIMPINEnv != "" {
		appendMapScalar(m, "sim_pin_env", d.SIMPINEnv)
	}
	if d.USBNetMode != nil {
		appendMapInt(m, "usbnet_mode", *d.USBNetMode)
	}
	if d.QMIUseProxy {
		appendMapBool(m, "qmi_use_proxy", true)
	}
	if d.QMIProxyPath != "" {
		appendMapScalar(m, "qmi_proxy_path", d.QMIProxyPath)
	}
	if d.QMIProxyExecutable != "" {
		appendMapScalar(m, "qmi_proxy_executable", d.QMIProxyExecutable)
	}
	if d.ProxyPort > 0 {
		appendMapInt(m, "proxy_port", d.ProxyPort)
	}

	return m
}

func getMapValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i < len(m.Content); i += 2 {
		k := m.Content[i]
		v := m.Content[i+1]
		if k != nil && k.Value == key {
			return v
		}
	}
	return nil
}

func getMapScalar(m *yaml.Node, key string) string {
	v := getMapValue(m, key)
	if v == nil {
		return ""
	}
	return v.Value
}

func setMapNode(m *yaml.Node, key string, val *yaml.Node) {
	if m == nil || m.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i < len(m.Content); i += 2 {
		k := m.Content[i]
		if k != nil && k.Value == key {
			m.Content[i+1] = val
			return
		}
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, val)
}

func deleteMapKey(m *yaml.Node, key string) {
	if m == nil || m.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i < len(m.Content); i += 2 {
		k := m.Content[i]
		if k != nil && k.Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return
		}
	}
}

func setMapScalar(m *yaml.Node, key, value string) {
	if value == "" {
		deleteMapKey(m, key)
		return
	}
	setMapNode(m, key, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
}

func setMapInt(m *yaml.Node, key string, value int) {
	setMapNode(m, key, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: fmt.Sprintf("%d", value)})
}

func setMapBool(m *yaml.Node, key string, value bool) {
	val := "false"
	if value {
		val = "true"
	}
	setMapNode(m, key, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: val})
}

func appendMapScalar(m *yaml.Node, key, value string) {
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value},
	)
}

func appendMapInt(m *yaml.Node, key string, value int) {
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: fmt.Sprintf("%d", value)},
	)
}

func appendMapBool(m *yaml.Node, key string, value bool) {
	val := "false"
	if value {
		val = "true"
	}
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: val},
	)
}
