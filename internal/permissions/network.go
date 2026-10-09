package permissions

import (
	"context"
	"fmt"

	"github.com/BigSmartie/Coding-Agent/internal/egress"
	"github.com/BigSmartie/Coding-Agent/internal/safety"
)

func canonicalNetworkOrigin(raw string) (string, error) {
	origin, u, err := egress.Origin(raw)
	if err != nil || u.Path != "" || u.RawPath != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("network grant must be an exact HTTPS origin")
	}
	return origin, nil
}

// EnsureWebRequest requires a fresh approval of the complete URL before a
// model-triggered web read. An origin grant alone cannot approve arbitrary
// paths or query strings, which may disclose data to that origin.
func (m *Manager) EnsureWebRequest(ctx context.Context, rawURL string) error {
	if len(rawURL) == 0 || len(rawURL) > 2048 {
		return fmt.Errorf("web URL must be 1–2048 bytes")
	}
	origin, _, err := egress.Origin(rawURL)
	if err != nil {
		return err
	}
	if m.prompt == nil {
		return fmt.Errorf("web request requires interactive approval")
	}
	result, err := m.prompt(ctx, Request{
		Kind: KindNetwork, Summary: "MyCode wants to read an external web URL",
		Details: []string{"origin: " + origin, "URL: " + safety.Redact(ctx, rawURL), "The URL path and query will be sent to this site. Returned content is untrusted."},
		Scope:   origin + ":web-read",
		Choices: []Choice{{Key: "n", Label: "deny once (default)", Decision: DecisionDenyOnce}, {Key: "y", Label: "allow this URL once", Decision: DecisionAllowOnce}},
	})
	if err != nil {
		return err
	}
	if result.Decision != DecisionAllowOnce {
		return fmt.Errorf("web request denied: %s", origin)
	}
	return nil
}

// EnsureNetwork grants a single HTTPS origin; paths and subdomains are never
// implicitly included in a persisted grant.
func (m *Manager) EnsureNetwork(ctx context.Context, origin, method string) error {
	canonical, err := canonicalNetworkOrigin(origin)
	if err != nil {
		return err
	}
	if m.deniedNetworkOrigins[canonical] {
		return fmt.Errorf("Network origin denied: %s", canonical)
	}
	if m.allowedNetworkOrigins[canonical] {
		return nil
	}
	if m.prompt == nil {
		return fmt.Errorf("Network origin %s requires approval in TTY mode", canonical)
	}
	result, err := m.prompt(ctx, Request{
		Kind: KindNetwork, Summary: "MyCode wants to contact an external HTTPS origin",
		Details: []string{"origin: " + canonical, "method: " + method, "Redirects and private IP addresses are blocked."}, Scope: canonical,
		Choices: []Choice{{Key: "n", Label: "deny once (default)", Decision: DecisionDenyOnce}, {Key: "y", Label: "allow once", Decision: DecisionAllowOnce}, {Key: "a", Label: "always allow this exact origin", Decision: DecisionAllowAlways}, {Key: "d", Label: "always deny this exact origin", Decision: DecisionDenyAlways}},
	})
	if err != nil {
		return err
	}
	switch result.Decision {
	case DecisionAllowOnce:
		return nil
	case DecisionAllowAlways:
		m.allowedNetworkOrigins[canonical] = true
		return m.persist()
	case DecisionDenyAlways:
		m.deniedNetworkOrigins[canonical] = true
		_ = m.persist()
	}
	return fmt.Errorf("Network origin denied: %s", canonical)
}

// EnsureRemoteMCP approves a single operation against an already trusted
// remote server. Origin approval alone does not authorize server-side effects.
func (m *Manager) EnsureRemoteMCP(ctx context.Context, server, operation, endpoint, arguments string) error {
	origin, err := canonicalNetworkOrigin(endpoint)
	if err != nil {
		return err
	}
	if m.prompt == nil {
		return fmt.Errorf("Remote MCP operation %s on %s requires interactive approval", operation, server)
	}
	arguments = safety.Redact(ctx, arguments)
	if len(arguments) > 4096 {
		arguments = arguments[:4096] + "…"
	}
	result, err := m.prompt(ctx, Request{
		Kind: KindMCP, Summary: "MyCode wants to call an external MCP server",
		Details: []string{"server: " + server, "origin: " + origin, "operation: " + operation, "arguments: " + arguments, "This operation may change external state."},
		Scope:   server + ":" + operation,
		Choices: []Choice{{Key: "n", Label: "deny once (default)", Decision: DecisionDenyOnce}, {Key: "y", Label: "allow this call once", Decision: DecisionAllowOnce}},
	})
	if err != nil {
		return err
	}
	if result.Decision != DecisionAllowOnce {
		return fmt.Errorf("Remote MCP operation denied: %s on %s", operation, server)
	}
	return nil
}
