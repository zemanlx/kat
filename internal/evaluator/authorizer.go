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

// NewMockAuthorizer creates a new mock authorizer.
func NewMockAuthorizer() *MockAuthorizer {
	return &MockAuthorizer{
		decisions: make(map[string]authorizer.Decision),
	}
}

// Allow configures the mock to allow a specific request.
func (m *MockAuthorizer) Allow(namespace, resource, verb string) {
	key := fmt.Sprintf("%s/%s/%s", namespace, resource, verb)
	m.decisions[key] = authorizer.DecisionAllow
}

// Deny configures the mock to deny a specific request.
func (m *MockAuthorizer) Deny(namespace, resource, verb string) {
	key := fmt.Sprintf("%s/%s/%s", namespace, resource, verb)
	m.decisions[key] = authorizer.DecisionDeny
}

// Authorize implements the authorizer.Authorizer interface.
func (m *MockAuthorizer) Authorize(_ context.Context, attrs authorizer.Attributes) (authorizer.Decision, string, error) {
	key := fmt.Sprintf("%s/%s/%s", attrs.GetNamespace(), attrs.GetResource(), attrs.GetVerb())
	if decision, ok := m.decisions[key]; ok {
		if decision == authorizer.DecisionAllow {
			return decision, "allowed by mock", nil
		}

		return decision, "denied by mock", nil
	}

	return authorizer.DecisionNoOpinion, "no opinion", nil
}

// MockUserInfo creates a simple user.Info for testing.
func MockUserInfo(username string, groups []string) user.Info {
	return &user.DefaultInfo{
		Name:   username,
		Groups: groups,
	}
}
