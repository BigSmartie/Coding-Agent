package permissions

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/BigSmartie/Coding-Agent/internal/safety"
)

func canonicalNetworkOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawPath != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("network grant must be an exact HTTPS origin")
	}
	host := strings.ToLower(u.Hostname())
	if strings.HasSuffix(host, ".") {
		return "", fmt.Errorf("network origin cannot have a trailing dot")
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port := u.Port(); port != "" && port != "443" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return "", fmt.Errorf("invalid network origin port")
		}
		host += ":" + strconv.Itoa(number)
	}
	return "https://" + host, nil
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
