// Package auth provides Azure Active Directory authentication helpers for
// Azure DevOps API clients.
//
// It supports acquiring OAuth2 access tokens for an Azure AD Service Principal
// using the client-credentials flow (client_id + client_secret). The acquired
// token can be used as a Bearer token in place of a Personal Access Token (PAT)
// when constructing an Azure DevOps API connection.
//
// Azure DevOps resource ID (scope):
//
//	499b84ac-1321-427f-aa17-267ca6975798/.default
//
// Token endpoint:
//
//	https://login.microsoftonline.com/{tenantID}/oauth2/v2.0/token
package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

const (
	// ADOResourceScope is the OAuth2 scope required to call the Azure DevOps REST API.
	// This is the well-known Azure DevOps resource ID registered in Azure AD.
	ADOResourceScope = "499b84ac-1321-427f-aa17-267ca6975798/.default"

	// tokenEndpointTemplate is the Azure AD OAuth2 v2.0 token endpoint URL template.
	// The tenant ID is substituted at runtime.
	tokenEndpointTemplate = "https://login.microsoftonline.com/%s/oauth2/v2.0/token"
)

// ServicePrincipalConfig holds the credentials for an Azure AD Service Principal
// using the client-credentials flow.
//
// All three fields are required. They are typically sourced from environment
// variables or a secrets manager (e.g. Jenkins credentials store, GCP Secret Manager).
type ServicePrincipalConfig struct {
	// TenantID is the Azure AD tenant (directory) ID that owns the Service Principal.
	// Example: "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx"
	TenantID string

	// ClientID is the application (client) ID of the registered Azure AD application.
	// Example: "yyyyyyyy-yyyy-yyyy-yyyy-yyyyyyyyyyyy"
	ClientID string

	// ClientSecret is the client secret value generated for the Azure AD application.
	// This value is sensitive and must never be logged or printed.
	ClientSecret string
}

// Validate checks that all required fields of ServicePrincipalConfig are set.
// Returns a descriptive error listing every missing field.
func (c ServicePrincipalConfig) Validate() error {
	var missing []string
	if c.TenantID == "" {
		missing = append(missing, "TenantID")
	}
	if c.ClientID == "" {
		missing = append(missing, "ClientID")
	}
	if c.ClientSecret == "" {
		missing = append(missing, "ClientSecret")
	}
	if len(missing) > 0 {
		return fmt.Errorf("service principal config is incomplete, missing fields: %s", strings.Join(missing, ", "))
	}
	return nil
}

// TokenSource is the interface for acquiring Azure AD access tokens.
// It is defined here to allow mocking in tests.
type TokenSource interface {
	// ADOToken returns a valid Azure AD Bearer token scoped to the Azure DevOps API.
	// Implementations must handle token refresh transparently.
	ADOToken(ctx context.Context) (string, error)
}

// ServicePrincipalTokenSource acquires Azure AD access tokens for the Azure DevOps
// API using the OAuth2 client-credentials flow.
//
// It wraps golang.org/x/oauth2 and caches the token until it expires, so
// repeated calls within the token's lifetime do not make additional network requests.
type ServicePrincipalTokenSource struct {
	config      ServicePrincipalConfig
	tokenSource oauth2.TokenSource
}

// NewServicePrincipalTokenSource creates a new ServicePrincipalTokenSource.
// It validates the config and pre-configures the underlying oauth2.TokenSource
// with the Azure AD client-credentials endpoint.
//
// The returned TokenSource is safe for concurrent use. Tokens are cached and
// refreshed automatically by the underlying oauth2 library.
func NewServicePrincipalTokenSource(ctx context.Context, config ServicePrincipalConfig) (*ServicePrincipalTokenSource, error) {
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("cannot create service principal token source: %w", err)
	}

	tokenURL := fmt.Sprintf(tokenEndpointTemplate, config.TenantID)

	// oauth2.Config with client-credentials grant.
	// We use Endpoint.TokenURL directly; AuthURL is not needed for this flow.
	oauthConfig := &oauth2.Config{
		ClientID:     config.ClientID,
		ClientSecret: config.ClientSecret,
		Endpoint: oauth2.Endpoint{
			TokenURL:  tokenURL,
			AuthStyle: oauth2.AuthStyleInParams,
		},
		Scopes: []string{ADOResourceScope},
	}

	// ClientCredentials returns a TokenSource that uses the client-credentials
	// grant. It caches the token and refreshes it before expiry.
	ts := oauthConfig.TokenSource(ctx, nil)
	// Wrap with ReuseTokenSource to ensure the cached token is reused across calls.
	reuseTS := oauth2.ReuseTokenSource(nil, ts)

	return &ServicePrincipalTokenSource{
		config:      config,
		tokenSource: reuseTS,
	}, nil
}

// ADOToken returns a valid Azure AD Bearer token scoped to the Azure DevOps API.
//
// The token is cached by the underlying oauth2.ReuseTokenSource and refreshed
// automatically when it expires. The first call performs a network request to
// the Azure AD token endpoint; subsequent calls within the token lifetime are
// served from cache.
//
// The returned string is the raw access token value, suitable for use as:
//
//	"Bearer " + token
func (s *ServicePrincipalTokenSource) ADOToken(ctx context.Context) (string, error) {
	token, err := s.tokenSource.Token()
	if err != nil {
		return "", fmt.Errorf("failed to acquire Azure AD token for ADO: %w", err)
	}
	if !token.Valid() {
		return "", fmt.Errorf("acquired Azure AD token is invalid or already expired")
	}
	return token.AccessToken, nil
}

// HTTPClientOption allows injecting a custom http.Client into the token source.
// This is used in tests to avoid real network calls.
type HTTPClientOption func(*http.Client)

// NewServicePrincipalTokenSourceWithHTTPClient creates a ServicePrincipalTokenSource
// that uses the provided http.Client for token endpoint requests.
// This is primarily intended for testing.
func NewServicePrincipalTokenSourceWithHTTPClient(
	ctx context.Context,
	config ServicePrincipalConfig,
	httpClient *http.Client,
) (*ServicePrincipalTokenSource, error) {
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("cannot create service principal token source: %w", err)
	}

	// Inject the custom HTTP client into the context so oauth2 uses it.
	ctx = context.WithValue(ctx, oauth2.HTTPClient, httpClient)

	tokenURL := fmt.Sprintf(tokenEndpointTemplate, config.TenantID)

	oauthConfig := &oauth2.Config{
		ClientID:     config.ClientID,
		ClientSecret: config.ClientSecret,
		Endpoint: oauth2.Endpoint{
			TokenURL:  tokenURL,
			AuthStyle: oauth2.AuthStyleInParams,
		},
		Scopes: []string{ADOResourceScope},
	}

	ts := oauthConfig.TokenSource(ctx, nil)
	reuseTS := oauth2.ReuseTokenSource(nil, ts)

	return &ServicePrincipalTokenSource{
		config:      config,
		tokenSource: reuseTS,
	}, nil
}

// ParseTokenEndpointURL returns the Azure AD token endpoint URL for the given tenant.
// Exported for use in tests and diagnostics.
func ParseTokenEndpointURL(tenantID string) (string, error) {
	endpoint := fmt.Sprintf(tokenEndpointTemplate, tenantID)
	if _, err := url.ParseRequestURI(endpoint); err != nil {
		return "", fmt.Errorf("invalid token endpoint URL for tenant %q: %w", tenantID, err)
	}
	return endpoint, nil
}

// TokenExpiresIn returns the duration until the given token expires.
// Returns zero and an error if the token is already expired or invalid.
// This is a utility function for logging and diagnostics.
func TokenExpiresIn(token *oauth2.Token) (time.Duration, error) {
	if token == nil {
		return 0, fmt.Errorf("token is nil")
	}
	if !token.Valid() {
		return 0, fmt.Errorf("token is expired or invalid")
	}
	return time.Until(token.Expiry), nil
}
