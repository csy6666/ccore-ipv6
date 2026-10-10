package cloudflare

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var placeholder = regexp.MustCompile(`\{(n|i)(?::(\d+))?\}`)
var label = regexp.MustCompile(`^[a-z0-9_][a-z0-9_-]*[a-z0-9_]$|^[a-z0-9_]$`)

func ExpandNames(template, zone string, count int) ([]string, error) {
	zone = strings.ToLower(strings.Trim(strings.TrimSpace(zone), "."))
	if zone == "" {
		return nil, errors.New("zone name is empty")
	}
	if count <= 0 {
		return nil, errors.New("count must be positive")
	}
	template = strings.TrimSpace(template)
	if template == "" {
		template = "@"
	}
	result := make([]string, 0, count)
	for i := 0; i < count; i++ {
		name := placeholder.ReplaceAllStringFunc(template, func(value string) string {
			parts := placeholder.FindStringSubmatch(value)
			index := i
			if parts[1] == "n" {
				index++
			}
			if parts[2] != "" {
				width, err := strconv.Atoi(parts[2])
				if err == nil && width > 0 && width <= 12 {
					return fmt.Sprintf("%0*d", width, index)
				}
			}
			return strconv.Itoa(index)
		})
		qualified, err := qualify(name, zone)
		if err != nil {
			return nil, err
		}
		result = append(result, qualified)
	}
	return result, nil
}

func qualify(name, zone string) (string, error) {
	name = strings.ToLower(strings.Trim(strings.TrimSpace(name), "."))
	if name == "" || name == "@" {
		return zone, nil
	}
	if name != zone && !strings.HasSuffix(name, "."+zone) {
		name += "." + zone
	}
	if len(name) > 253 {
		return "", errors.New("DNS name is too long")
	}
	for _, part := range strings.Split(name, ".") {
		if len(part) > 63 || !label.MatchString(part) {
			return "", fmt.Errorf("invalid DNS label %q", part)
		}
	}
	return name, nil
}
