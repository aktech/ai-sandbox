package proxy

import "encoding/json"

// Config is the parsed `proxy` block from config.json.
type Config struct {
	Allow  []string              `json:"allow"`
	Inject map[string]InjectRule `json:"inject"`
}

// inlineRule mirrors InjectRule for JSON (lowercase tags).
type inlineRule struct {
	Header string `json:"header"`
	Secret string `json:"secret"`
	Format string `json:"format,omitempty"`
	Basic  bool   `json:"basic,omitempty"`
}

func ParseConfig(data []byte) (*Config, error) {
	var aux struct {
		Allow  []string              `json:"allow"`
		Inject map[string]inlineRule `json:"inject"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return nil, err
	}
	c := &Config{Allow: aux.Allow, Inject: map[string]InjectRule{}}
	for host, r := range aux.Inject {
		c.Inject[host] = InjectRule{Header: r.Header, Secret: r.Secret, Format: r.Format, Basic: r.Basic}
	}
	return c, nil
}

// EffectiveAllow returns Allow plus every inject host (inject implies allow).
func (c *Config) EffectiveAllow() []string {
	seen := map[string]bool{}
	var out []string
	add := func(h string) {
		if !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	for _, h := range c.Allow {
		add(h)
	}
	for h := range c.Inject {
		add(h)
	}
	return out
}

// SecretNames returns the distinct secret keys referenced by inject rules.
func (c *Config) SecretNames() []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range c.Inject {
		if !seen[r.Secret] {
			seen[r.Secret] = true
			out = append(out, r.Secret)
		}
	}
	return out
}
