//go:build !darwin

package privacy

func Status() map[string]string {
	return map[string]string{
		"screen":        "unknown",
		"accessibility": "unknown",
		"automation":    "unknown",
	}
}
