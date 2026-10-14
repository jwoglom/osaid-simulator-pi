package config

import (
	"bytes"
	"fmt"
	"strconv"

	"gopkg.in/yaml.v3"
)

// SetWifi rewrites only the wifi block of raw YAML, preserving every comment
// and the rest of the document byte-for-byte as far as yaml.v3 allows.
func SetWifi(raw []byte, w Wifi) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("config is not a mapping")
	}
	root := doc.Content[0]
	wifi := mappingGet(root, "wifi")
	if wifi == nil {
		wifi = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "wifi"}, wifi)
	}
	mappingSet(wifi, "enabled", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(w.Enabled)})
	mappingSet(wifi, "ssid", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: w.SSID, Style: yaml.DoubleQuotedStyle})
	mappingSet(wifi, "psk", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: w.PSK, Style: yaml.DoubleQuotedStyle})
	if w.Country != "" {
		mappingSet(wifi, "country", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: w.Country})
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	_ = enc.Close()
	if _, err := Parse(buf.Bytes()); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func mappingGet(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func mappingSet(m *yaml.Node, key string, v *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			v.HeadComment, v.LineComment = m.Content[i+1].HeadComment, m.Content[i+1].LineComment
			m.Content[i+1] = v
			return
		}
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, v)
}
