package evaluator

import (
	"context"
	"fmt"

	"github.com/google/cel-go/common/types/ref"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/cel/library"
)

// NewAuthorizerValue creates a CEL authorizer value using Kubernetes' library function.
// This wraps a Kubernetes authorizer for use in CEL expressions.
func NewAuthorizerValue(auth authorizer.Authorizer, userInfo user.Info) ref.Val {
	if auth == nil || userInfo == nil {
		return nil
	}

	return library.NewAuthorizerVal(userInfo, auth)
}

// MockAuthorizer is a simple mock authorizer for testing.
type MockAuthorizer struct {
	decisions map[string]authorizer.Decision
}

// AuthorizationMockConfig represents a mocked authorization decision configuration.
type AuthorizationMockConfig struct {
	Group       string `json:"group,omitempty"`
	Resource    string `json:"resource"`
	Subresource string `json:"subresource,omitempty"`
	Namespace   string `json:"namespace,omitempty"`
	Verb        string `json:"verb"`
	Decision    string `json:"decision"` // "allow" or "deny"
}

// NewMockAuthorizer creates a new mock authorizer.
func NewMockAuthorizer() *MockAuthorizer {
	return &MockAuthorizer{
		decisions: make(map[string]authorizer.Decision),
	}
}

// NewMockAuthorizerFromConfig creates a mock authorizer from a list of configs.
func NewMockAuthorizerFromConfig(configs []AuthorizationMockConfig) *MockAuthorizer {
	m := NewMockAuthorizer()
	for _, c := range configs {
		m.Add(c)
	}
	return m
}

// Add adds a decision to the mock authorizer.
func (m *MockAuthorizer) Add(c AuthorizationMockConfig) {
	key := fmt.Sprintf("%s/%s/%s/%s/%s", c.Group, c.Resource, c.Subresource, c.Namespace, c.Verb)
	if c.Decision == "allow" {
		m.decisions[key] = authorizer.DecisionAllow
	} else {
		m.decisions[key] = authorizer.DecisionDeny
	}
}

// Allow configures the mock to allow a specific request.
func (m *MockAuthorizer) Allow(group, resource, subresource, namespace, verb string) {
	key := fmt.Sprintf("%s/%s/%s/%s/%s", group, resource, subresource, namespace, verb)
	m.decisions[key] = authorizer.DecisionAllow
}

// Deny configures the mock to deny a specific request.
func (m *MockAuthorizer) Deny(group, resource, subresource, namespace, verb string) {
	key := fmt.Sprintf("%s/%s/%s/%s/%s", group, resource, subresource, namespace, verb)
	m.decisions[key] = authorizer.DecisionDeny
}

// Authorize implements the authorizer.Authorizer interface.
func (m *MockAuthorizer) Authorize(_ context.Context, attrs authorizer.Attributes) (authorizer.Decision, string, error) {
	// Try specific match
	key := fmt.Sprintf("%s/%s/%s/%s/%s", attrs.GetAPIGroup(), attrs.GetResource(), attrs.GetSubresource(), attrs.GetNamespace(), attrs.GetVerb())
	if decision, ok := m.decisions[key]; ok {
		return decision, "mock decision", nil
	}

	// Try match allowing empty namespace in config to mean all namespaces (optional enhancement, but keeping simple for now)

	return authorizer.DecisionNoOpinion, "no opinion", nil
}

// MockUserInfo creates a simple user.Info for testing.
func MockUserInfo(username string, groups []string) user.Info {
	return &user.DefaultInfo{
		Name:   username,
		Groups: groups,
	}
}
