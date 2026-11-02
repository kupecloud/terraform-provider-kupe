package client

import (
	"context"
	"errors"
	"net/http"
)

// App is the tenant's app identity. Environments have independent lifecycles.
type App struct {
	Name               string     `json:"name"`
	UID                string     `json:"uid"`
	Generation         int64      `json:"generation"`
	ResourceVersion    string     `json:"resourceVersion"`
	CreatedAt          string     `json:"createdAt"`
	DeletionProtection bool       `json:"deletionProtection"`
	Status             *AppStatus `json:"status,omitempty"`
}

type AppStatus struct {
	Phase              string                  `json:"phase"`
	ObservedGeneration int64                   `json:"observedGeneration"`
	Conditions         []Condition             `json:"conditions"`
	Environments       []AppEnvironmentSummary `json:"environments"`
}

type AppEnvironmentSummary struct {
	Name    string `json:"name"`
	Phase   string `json:"phase"`
	Address string `json:"address"`
}

type CreateAppRequest struct {
	Name               string `json:"name"`
	DeletionProtection *bool  `json:"deletionProtection,omitempty"`
}

type PatchAppRequest struct {
	DeletionProtection *bool `json:"deletionProtection,omitempty"`
}

// AppEnvironment is the flat public environment response, never a CRD.
type AppEnvironment struct {
	Name               string                `json:"name"`
	UID                string                `json:"uid"`
	App                string                `json:"app"`
	Environment        string                `json:"environment"`
	Cluster            string                `json:"cluster"`
	DeletionProtection bool                  `json:"deletionProtection"`
	DeploymentRevision int64                 `json:"deploymentRevision"`
	Web                *AppWeb               `json:"web"`
	Secrets            []AppSecret           `json:"secrets"`
	Domains            []string              `json:"domains"`
	Generation         int64                 `json:"generation"`
	ResourceVersion    string                `json:"resourceVersion"`
	CreatedAt          string                `json:"createdAt"`
	Status             *AppEnvironmentStatus `json:"status,omitempty"`
}

type AppQuantities struct {
	CPU    string `json:"cpu"`
	Memory string `json:"memory"`
}

type AppResources struct {
	Requests AppQuantities `json:"requests"`
	Limits   AppQuantities `json:"limits"`
}

// AppWeb is sent in full on PATCH. In particular Env must encode [] when
// cleared: omitting it from a merge patch would retain the previous list.
type AppWeb struct {
	Image        string          `json:"image"`
	Port         int32           `json:"port"`
	Replicas     int32           `json:"replicas"`
	Resources    AppResources    `json:"resources"`
	Command      []string        `json:"command"`
	Args         []string        `json:"args"`
	Env          []AppEnvVar     `json:"env"`
	HealthChecks AppHealthChecks `json:"healthChecks"`
}

type AppEnvVar struct {
	Name      string        `json:"name"`
	Value     *string       `json:"value,omitempty"`
	ValueFrom *AppValueFrom `json:"valueFrom,omitempty"`
}

type AppValueFrom struct {
	SecretKeyRef AppSecretKeyRef `json:"secretKeyRef"`
}
type AppSecretKeyRef struct {
	Secret string `json:"secret"`
	Key    string `json:"key"`
}

type AppHealthChecks struct {
	Readiness *AppProbe `json:"readiness,omitempty"`
	// An explicit null removes a previously configured liveness probe.
	Liveness *AppProbe `json:"liveness"`
}

type AppProbe struct {
	TCP  *AppTCPProbe  `json:"tcp"`
	HTTP *AppHTTPProbe `json:"http"`
}

type AppTCPProbe struct{}
type AppHTTPProbe struct {
	Path string `json:"path"`
}
type AppSecret struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type AppReleaseRef struct {
	Name string `json:"name"`
}
type AppDeployment struct {
	Release *AppReleaseRef `json:"release"`
	Phase   string         `json:"phase"`
	Reason  string         `json:"reason"`
}

type AppRecovery struct {
	Phase   string         `json:"phase"`
	Release *AppReleaseRef `json:"release"`
}

type AppEnvironmentStatus struct {
	Phase                 string         `json:"phase"`
	ObservedGeneration    int64          `json:"observedGeneration"`
	Conditions            []Condition    `json:"conditions"`
	Namespace             string         `json:"namespace"`
	Address               string         `json:"address"`
	RequestedRelease      *AppReleaseRef `json:"requestedRelease"`
	ActiveRelease         *AppReleaseRef `json:"activeRelease"`
	LastSuccessfulRelease *AppReleaseRef `json:"lastSuccessfulRelease"`
	Deployment            *AppDeployment `json:"deployment"`
	Recovery              *AppRecovery   `json:"recovery"`
}

type CreateAppEnvironmentRequest struct {
	Environment        string      `json:"environment"`
	Cluster            string      `json:"cluster"`
	DeletionProtection *bool       `json:"deletionProtection,omitempty"`
	Web                *AppWeb     `json:"web"`
	Secrets            []AppSecret `json:"secrets"`
	Domains            []string    `json:"domains"`
}

// PatchAppEnvironmentRequest replaces each list with the complete plan.
// DeploymentRevision is deliberately absent: only the retry route changes it.
// Pointers distinguish omitted lists from an explicit empty replacement.
type PatchAppEnvironmentRequest struct {
	Web                *AppWeb      `json:"web,omitempty"`
	Secrets            *[]AppSecret `json:"secrets,omitempty"`
	Domains            *[]string    `json:"domains,omitempty"`
	DeletionProtection *bool        `json:"deletionProtection,omitempty"`
}

type AppRetryResponse struct {
	App                string `json:"app"`
	Environment        string `json:"environment"`
	DeploymentRevision int64  `json:"deploymentRevision"`
	Generation         int64  `json:"generation"`
}

func (c *Client) GetApp(ctx context.Context, name string) (*App, string, error) {
	var app App
	etag, err := c.request(ctx, http.MethodGet, c.tenantPath("apps", name), nil, &app)
	if err != nil {
		return nil, "", err
	}
	return &app, etag, nil
}

func (c *Client) CreateApp(ctx context.Context, req CreateAppRequest) (*App, string, error) {
	var app App
	etag, err := c.request(ctx, http.MethodPost, c.tenantPath("apps"), req, &app)
	if err != nil {
		return nil, "", err
	}
	return &app, etag, nil
}

func (c *Client) PatchApp(ctx context.Context, name, etag string, req PatchAppRequest) (*App, string, error) {
	if etag == "" {
		return nil, "", errors.New("an app ETag is required; refresh before applying")
	}
	var app App
	newETag, err := c.requestWithETag(ctx, http.MethodPatch, c.tenantPath("apps", name), etag, req, &app)
	if err != nil {
		return nil, "", err
	}
	return &app, newETag, nil
}

// DeleteApp reviews an empty child set. Terraform destroys its environment
// resources first; it never authorises deletion of unmanaged namespaces.
// Sending [] rather than omitting environments is a data-safety boundary.
func (c *Client) DeleteApp(ctx context.Context, name, etag string) error {
	if etag == "" {
		return errors.New("an app ETag is required; refresh before deleting")
	}
	req := struct {
		Confirm      string   `json:"confirm"`
		Environments []string `json:"environments"`
	}{Confirm: name, Environments: []string{}}
	_, err := c.requestWithETag(ctx, http.MethodDelete, c.tenantPath("apps", name), etag, req, nil)
	return err
}

func (c *Client) GetAppEnvironment(ctx context.Context, app, environment string) (*AppEnvironment, string, error) {
	var env AppEnvironment
	etag, err := c.request(ctx, http.MethodGet, c.tenantPath("apps", app, "environments", environment), nil, &env)
	if err != nil {
		return nil, "", err
	}
	return &env, etag, nil
}

func (c *Client) CreateAppEnvironment(ctx context.Context, app string, req CreateAppEnvironmentRequest) (*AppEnvironment, string, error) {
	var env AppEnvironment
	etag, err := c.request(ctx, http.MethodPost, c.tenantPath("apps", app, "environments"), req, &env)
	if err != nil {
		return nil, "", err
	}
	return &env, etag, nil
}

func (c *Client) PatchAppEnvironment(ctx context.Context, app, environment, etag string, req PatchAppEnvironmentRequest) (*AppEnvironment, string, error) {
	if etag == "" {
		return nil, "", errors.New("an environment ETag is required; refresh before applying")
	}
	var env AppEnvironment
	newETag, err := c.requestWithETag(ctx, http.MethodPatch, c.tenantPath("apps", app, "environments", environment), etag, req, &env)
	if err != nil {
		return nil, "", err
	}
	return &env, newETag, nil
}

func (c *Client) DeleteAppEnvironment(ctx context.Context, app, environment, etag string) error {
	if etag == "" {
		return errors.New("an environment ETag is required; refresh before deleting")
	}
	req := struct {
		Confirm string `json:"confirm"`
	}{Confirm: environment}
	_, err := c.requestWithETag(ctx, http.MethodDelete, c.tenantPath("apps", app, "environments", environment), etag, req, nil)
	return err
}

func (c *Client) RetryAppEnvironment(ctx context.Context, app, environment, etag string) (*AppRetryResponse, string, error) {
	if etag == "" {
		return nil, "", errors.New("an environment ETag is required; refresh before retrying")
	}
	var retry AppRetryResponse
	newETag, err := c.requestWithETag(ctx, http.MethodPost, c.tenantPath("apps", app, "environments", environment, "retry"), etag, nil, &retry)
	if err != nil {
		return nil, "", err
	}
	return &retry, newETag, nil
}

// AppRelease is an immutable, flat runtime snapshot. There is deliberately no
// accepted-generation field: the API does not publish one.
type AppRelease struct {
	Name               string            `json:"name"`
	Number             int64             `json:"number"`
	DeploymentRevision int64             `json:"deploymentRevision"`
	Trigger            string            `json:"trigger"`
	Runtime            AppReleaseRuntime `json:"runtime"`
	Status             AppReleaseStatus  `json:"status"`
}

type AppReleaseRuntime struct {
	AppWeb
	Secrets []AppReleaseSecret `json:"secrets"`
}

type AppReleaseSecret struct {
	Name string   `json:"name"`
	Path string   `json:"path"`
	Keys []string `json:"keys"`
}

type AppReleaseStatus struct {
	Phase       string              `json:"phase"`
	Reason      string              `json:"reason"`
	Message     string              `json:"message"`
	StartedAt   string              `json:"startedAt"`
	DeadlineAt  string              `json:"deadlineAt"`
	CompletedAt string              `json:"completedAt"`
	ImageDigest string              `json:"imageDigest"`
	Recovery    *AppReleaseRecovery `json:"recovery,omitempty"`
}

type AppReleaseRecovery struct {
	Phase       string         `json:"phase"`
	Release     *AppReleaseRef `json:"release"`
	Reason      string         `json:"reason"`
	StartedAt   string         `json:"startedAt"`
	DeadlineAt  string         `json:"deadlineAt"`
	CompletedAt string         `json:"completedAt"`
}

// ListAppReleases returns all retained releases, newest first. The API scopes
// the history to this environment UID; callers still recheck UID after reading.
func (c *Client) ListAppReleases(ctx context.Context, app, environment string) ([]AppRelease, error) {
	var response struct {
		Items []AppRelease `json:"items"`
	}
	_, err := c.request(ctx, http.MethodGet, c.tenantPath("apps", app, "environments", environment, "releases"), nil, &response)
	if err != nil {
		return nil, err
	}
	return response.Items, nil
}
