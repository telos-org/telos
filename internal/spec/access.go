package spec

import (
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"strings"
	"unicode/utf8"
)

// AccessSpec declares host-bound credentials and HTTPS permissions, or legacy access.
// Credential values are stored separately by the Cloud control plane.
type AccessSpec struct {
	Network          []EgressRule      `json:"network,omitempty"` // non-nil selects the host-bound schema, including an explicit []
	Integrations     []string          `json:"integrations"`
	Allowlist        []NetworkRule     `json:"allowlist"`
	IntegrationNames map[string]string `json:"integration_names,omitempty"` // review labels; IDs determine access
}

type EgressRule struct {
	NetworkRule
	Credentials string `json:"credentials,omitempty"`
}

func (a AccessSpec) MarshalJSON() ([]byte, error) {
	if a.Network != nil {
		return json.Marshal(struct {
			Network []EgressRule `json:"network"`
		}{a.Network})
	}
	type legacy AccessSpec
	return json.Marshal(legacy(a))
}

type NetworkRule struct {
	Host    string   `json:"host"`
	Methods []string `json:"methods"`
	Paths   []string `json:"paths"`
}

var (
	integrationIDRE           = regexp.MustCompile(`^(?:sec-[A-Za-z0-9-]{1,124}|sec_[A-Za-z0-9_-]{1,124})$`)
	integrationReservedNameRE = regexp.MustCompile(`(?i)^telos managed / `)
	networkLabelRE            = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	networkMethodRE           = regexp.MustCompile(`^[A-Z]+$`)
	networkPathRE             = regexp.MustCompile(`^/[A-Za-z0-9._~!$&'()*+,;=:@/*-]*$`)
)

// ParseAccess reads host-bound network or the legacy paired fields. A nil result means
// the spec has no access declaration; an empty declaration is explicit.
func ParseAccess(raw map[string]interface{}) (*AccessSpec, error) {
	integrations, hasIntegrations := raw["integrations"]
	allowlist, hasAllowlist := raw["allowlist"]
	network, hasNetwork := raw["network"]
	egress, hasEgress := raw["egress"]
	if hasNetwork || hasEgress {
		if (hasNetwork && hasEgress) || hasIntegrations || hasAllowlist {
			return nil, fmt.Errorf("use only one access format: 'network', legacy 'egress', or paired 'integrations' and 'allowlist'")
		}
		if hasEgress {
			network = egress
		}
		return parseEgress(network)
	}
	if !hasIntegrations && !hasAllowlist {
		return nil, nil
	}
	if !hasIntegrations || !hasAllowlist {
		return nil, fmt.Errorf("'integrations' and 'allowlist' must both be declared; use [] for an empty list")
	}
	ids, names, err := parseIntegrations(integrations)
	if err != nil {
		return nil, err
	}
	rows, ok := allowlist.([]interface{})
	if !ok || len(rows) > 10000 {
		return nil, fmt.Errorf("'allowlist' must be a list of at most 10000 rules")
	}
	access := &AccessSpec{Integrations: ids, IntegrationNames: names, Allowlist: make([]NetworkRule, 0, len(rows))}
	seen := make(map[string]bool, len(rows))
	for index, row := range rows {
		rule, err := parseNetworkRule(row)
		if err != nil {
			return nil, fmt.Errorf("allowlist rule %d: %w", index+1, err)
		}
		key, _ := json.Marshal(rule)
		if !seen[string(key)] {
			access.Allowlist = append(access.Allowlist, rule)
			seen[string(key)] = true
		}
	}
	return access, nil
}

func parseEgress(value interface{}) (*AccessSpec, error) {
	rows, ok := value.([]interface{})
	if !ok || len(rows) > 10000 {
		return nil, fmt.Errorf("'network' must be a list of at most 10000 rules")
	}
	access := &AccessSpec{Network: make([]EgressRule, 0, len(rows))}
	seen := make(map[string]bool, len(rows))
	credentials := map[string]bool{}
	for index, row := range rows {
		raw, ok := row.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("network rule %d: must be a mapping", index+1)
		}
		network := make(map[string]interface{}, len(raw))
		for key, value := range raw {
			if key != "credentials" {
				network[key] = value
			}
		}
		rule, err := parseNetworkRule(network)
		if err != nil {
			return nil, fmt.Errorf("network rule %d: %w", index+1, err)
		}
		for _, method := range rule.Methods {
			if len(method) > 16 {
				return nil, fmt.Errorf("network rule %d: methods must be at most 16 characters", index+1)
			}
		}
		entry := EgressRule{NetworkRule: rule}
		if value, exists := raw["credentials"]; exists {
			id, ok := value.(string)
			if !ok || !integrationIDRE.MatchString(id) {
				return nil, fmt.Errorf("network rule %d: credentials must be a workspace credential ID beginning with sec- or sec_", index+1)
			}
			if strings.HasPrefix(rule.Host, "*.") {
				return nil, fmt.Errorf("network rule %d: credentials require an exact hostname", index+1)
			}
			for _, path := range rule.Paths {
				if strings.Contains(strings.TrimSuffix(path, "/*"), "*") {
					return nil, fmt.Errorf("network rule %d: credential paths must be exact or end in /*", index+1)
				}
			}
			entry.Credentials = id
			credentials[id] = true
			if len(credentials) > 100 {
				return nil, fmt.Errorf("'network' may reference at most 100 unique credentials")
			}
		}
		key, _ := json.Marshal(entry)
		if !seen[string(key)] {
			access.Network = append(access.Network, entry)
			seen[string(key)] = true
		}
	}
	return access, nil
}

func parseIntegrations(value interface{}) ([]string, map[string]string, error) {
	items, ok := value.([]interface{})
	if !ok || len(items) > 100 {
		return nil, nil, fmt.Errorf("'integrations' must be a list of at most 100 unique integration references")
	}
	ids := make([]string, 0, len(items))
	names := map[string]string{}
	seen := map[string]bool{}
	for _, item := range items {
		id, isID := item.(string)
		if !isID {
			ref, ok := item.(map[string]interface{})
			if !ok || len(ref) != 2 {
				return nil, nil, fmt.Errorf("integration references must be IDs or mappings containing exactly id and name")
			}
			id, ok = ref["id"].(string)
			name, named := ref["name"].(string)
			name = strings.TrimSpace(name)
			if !ok || !named || name == "" || utf8.RuneCountInString(name) > 120 ||
				strings.ContainsFunc(name, func(r rune) bool { return r < 32 }) ||
				integrationReservedNameRE.MatchString(name) {
				return nil, nil, fmt.Errorf("integration references require an id and a nonempty name of at most 120 characters, without control characters or a reserved managed prefix")
			}
			names[id] = name
		}
		if !integrationIDRE.MatchString(id) {
			return nil, nil, fmt.Errorf("'integrations' must contain workspace credential IDs beginning with sec- or sec_")
		}
		if seen[id] {
			return nil, nil, fmt.Errorf("'integrations' must contain unique integration IDs")
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids, names, nil
}

// These checks match the Cloud deployment network policy. Cloud remains
// authoritative for authorization and availability of referenced integrations.
func parseNetworkRule(value interface{}) (NetworkRule, error) {
	var result NetworkRule
	raw, ok := value.(map[string]interface{})
	if !ok {
		return result, fmt.Errorf("must be a mapping with host, methods and paths")
	}
	for key := range raw {
		if key != "host" && key != "methods" && key != "paths" {
			return result, fmt.Errorf("unsupported field %q", key)
		}
	}
	host, ok := raw["host"].(string)
	if !ok {
		return result, fmt.Errorf("host must be a DNS hostname")
	}
	host = strings.ToLower(strings.TrimSpace(host))
	suffix := strings.TrimPrefix(host, "*.")
	labels := strings.Split(suffix, ".")
	if len(host) > 253 || len(labels) < 2 || net.ParseIP(suffix) != nil {
		return result, fmt.Errorf("host must be a DNS hostname, optionally beginning with *.")
	}
	for _, label := range labels {
		if !networkLabelRE.MatchString(label) {
			return result, fmt.Errorf("host must be a DNS hostname, optionally beginning with *.")
		}
	}
	result.Host = host
	result.Methods = []string{}
	result.Paths = []string{}
	var err error
	if value, exists := raw["methods"]; exists {
		result.Methods, err = accessStrings(value, "methods", 20)
		if err != nil {
			return result, err
		}
	}
	for _, method := range result.Methods {
		if !networkMethodRE.MatchString(method) {
			return result, fmt.Errorf("methods must be uppercase ASCII HTTP method names")
		}
	}
	if value, exists := raw["paths"]; exists {
		result.Paths, err = accessStrings(value, "paths", 100)
		if err != nil {
			return result, err
		}
	}
	for _, path := range result.Paths {
		if !networkPathRE.MatchString(path) || strings.Contains(path, "//") {
			return result, fmt.Errorf("paths must start with / and contain no URL escapes or dot segments")
		}
		for _, segment := range strings.Split(path, "/") {
			if segment == "." || segment == ".." {
				return result, fmt.Errorf("paths must contain no dot segments")
			}
		}
	}
	return result, nil
}

func accessStrings(value interface{}, field string, limit int) ([]string, error) {
	items, ok := value.([]interface{})
	if !ok || len(items) > limit {
		return nil, fmt.Errorf("'%s' must be a list of at most %d unique strings", field, limit)
	}
	result := make([]string, 0, len(items))
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok || text == "" || len(text) > 2048 || seen[text] {
			return nil, fmt.Errorf("'%s' must contain unique, nonempty strings of at most 2048 characters", field)
		}
		result = append(result, text)
		seen[text] = true
	}
	return result, nil
}
