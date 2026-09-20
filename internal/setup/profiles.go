package setup

import (
	"regexp"
	"strings"
)

// Read only profile topology; never include file contents in an error.
func parseProfiles(body []byte, credentials bool) (map[string]map[string]string, error) {
	profiles := map[string]map[string]string{}
	section := ""
	for _, raw := range strings.Split(string(body), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") {
				return nil, invalid("invalid AWS configuration section")
			}
			name := strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")
			section = ""
			if credentials {
				section = name
			} else if name == "default" {
				section = name
			} else if strings.HasPrefix(name, "profile ") {
				section = strings.TrimPrefix(name, "profile ")
			}
			if section != "" {
				if profiles[section] != nil {
					return nil, invalid("duplicate AWS profile section")
				}
				profiles[section] = map[string]string{}
			}
			continue
		}
		if section != "" {
			key, value, ok := strings.Cut(line, "=")
			if ok {
				key = strings.TrimSpace(key)
				if _, exists := profiles[section][key]; exists {
					return nil, invalid("duplicate AWS profile setting")
				}
				profiles[section][key] = strings.TrimSpace(value)
			}
		}
	}
	return profiles, nil
}

var processProfile = regexp.MustCompile(`--profile(?:=|\s+)(?:"([^"]+)"|'([^']+)'|([A-Za-z0-9_.@-]+))`)

func validateSourceProfiles(configuration, credentials []byte, source, bridge, operator string) error {
	profiles, err := parseProfiles(configuration, false)
	if err != nil {
		return err
	}
	secrets, err := parseProfiles(credentials, true)
	if err != nil {
		return err
	}
	if secrets[bridge] != nil || (operator != source && secrets[operator] != nil) {
		return fail("profile_conflict", "a generated profile name exists in the shared credentials file; choose distinct unused profile names")
	}
	for name, fields := range secrets {
		if profiles[name] == nil {
			profiles[name] = map[string]string{}
		}
		for k, v := range fields {
			profiles[name][k] = v
		}
	}
	seen := map[string]bool{}
	var visit func(string, int) error
	visit = func(name string, depth int) error {
		if depth > 16 || seen[name] || name == bridge || (name == operator && name != source) {
			return fail("profile_recursion", "source credentials refer back to a managed setup/operator profile or form a cycle; choose an independent source")
		}
		seen[name] = true
		defer delete(seen, name)
		fields := profiles[name]
		if fields == nil {
			return fail("source_profile_missing", "selected source profile does not exist in the AWS configuration or credentials file")
		}
		if parent := fields["source_profile"]; parent != "" {
			if err := visit(parent, depth+1); err != nil {
				return err
			}
		}
		for _, match := range processProfile.FindAllStringSubmatch(fields["credential_process"], -1) {
			for _, target := range match[1:] {
				if target != "" {
					if err := visit(target, depth+1); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}
	return visit(source, 0)
}

func sameSourceCaller(a, b string) bool {
	if a == b {
		return true
	}
	if strings.Contains(a, ":assumed-role/") && strings.Contains(b, ":assumed-role/") {
		return a[:strings.LastIndex(a, "/")] == b[:strings.LastIndex(b, "/")]
	}
	return false
}
