package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sort"

	"github.com/kupecloud/terraform-provider-kupe/internal/client"
)

type mockAppEnvironmentRequest struct {
	method, path, etag string
	body               map[string]any
}

type mockAppEnvironmentState struct {
	appEnvironments                map[string]*client.AppEnvironment
	appReleases                    map[string][]client.AppRelease
	appEnvironmentRequests         []mockAppEnvironmentRequest
	appEnvironmentOutcome          string
	appEnvironmentRecovery         bool
	appEnvironmentReadsUnavailable bool
	appEnvironmentNamespaceInUse   bool
	appEnvironmentBeforePatch      []func(*client.AppEnvironment)
	appEnvironmentOnGet            []func(*client.AppEnvironment, *[]client.AppRelease)
	appEnvironmentAfterWrite       []func(*client.AppEnvironment, *[]client.AppRelease)
	appEnvironmentDeleteLag        int
	appEnvironmentDeleting         map[string]int
	appEnvironmentHistoryHook      func(*client.AppEnvironment)
	// appDatabasePhase is each connection's status phase after a release;
	// empty means Ready.
	appDatabasePhase string
}

func newMockAppEnvironmentState() mockAppEnvironmentState {
	return mockAppEnvironmentState{appEnvironments: map[string]*client.AppEnvironment{}, appReleases: map[string][]client.AppRelease{}, appEnvironmentDeleting: map[string]int{}}
}

// The existing mock's mutex covers this route and every scripted transition.
func (m *mockKupeAPI) serveAppEnvironment(w http.ResponseWriter, r *http.Request, app string, parts []string) {
	if len(parts) == 0 || parts[0] != "environments" {
		mockNotFound(w, "app", app)
		return
	}
	if _, exists := m.apps[app]; !exists {
		mockNotFound(w, "app", app)
		return
	}
	name := ""
	if len(parts) > 1 {
		name = parts[1]
	}
	key := app + "/" + name
	var body map[string]any
	if r.Body != nil && r.ContentLength != 0 {
		mustDecodeJSON(r, &body)
	}
	m.appEnvironmentRequests = append(m.appEnvironmentRequests, mockAppEnvironmentRequest{r.Method, r.URL.Path, r.Header.Get("If-Match"), body})
	if m.appEnvironmentReadsUnavailable && r.Method == http.MethodGet || m.appsDisabled && r.Method == http.MethodPost {
		mockError(w, http.StatusNotFound, client.CodeAppsUnavailable, "apps are not available on this platform")
		return
	}
	if name == "" && r.Method == http.MethodPost {
		data, err := json.Marshal(body)
		if err != nil {
			panic(err)
		}
		var request client.CreateAppEnvironmentRequest
		if err := json.Unmarshal(data, &request); err != nil {
			panic(err)
		}
		key = app + "/" + request.Environment
		if m.appEnvironments[key] != nil {
			mockError(w, http.StatusConflict, "", "environment already exists")
			return
		}
		env := &client.AppEnvironment{App: app, Environment: request.Environment, Cluster: request.Cluster, UID: "env-" + m.nextRV(), Name: app + "-" + request.Environment, Generation: 1, DeploymentRevision: 1, Runtime: request.Runtime, Secrets: request.Secrets, Databases: mockDefaultAppDatabases(request.Databases), Domains: request.Domains, DeletionProtection: request.DeletionProtection == nil || *request.DeletionProtection, Status: &client.AppEnvironmentStatus{Phase: "Provisioning", Namespace: "app-" + app + "-" + request.Environment, Address: "https://" + app + "-" + request.Environment + ".example.com"}}
		m.appEnvironments[key] = env
		m.appMockRelease(key, env)
		m.appEnvironmentOnGet = m.appEnvironmentAfterWrite
		m.appEnvironmentAfterWrite = nil
		w.Header().Set("ETag", mockAppEnvironmentETag(env))
		w.WriteHeader(http.StatusCreated)
		mustEncodeJSON(w, env)
		return
	}
	env := m.appEnvironments[key]
	if env == nil {
		mockNotFound(w, "app environment", name)
		return
	}
	if len(parts) == 3 && parts[2] == "releases" && r.Method == http.MethodGet {
		if m.appEnvironmentHistoryHook != nil {
			hook := m.appEnvironmentHistoryHook
			m.appEnvironmentHistoryHook = nil
			hook(env)
		}
		items := m.appReleases[key]
		mustEncodeJSON(w, map[string]any{"items": items})
		return
	}
	if r.Method == http.MethodGet {
		if remaining, deleting := m.appEnvironmentDeleting[key]; deleting {
			if remaining == 0 {
				delete(m.appEnvironments, key)
				delete(m.appEnvironmentDeleting, key)
				mockNotFound(w, "app environment", name)
				return
			}
			m.appEnvironmentDeleting[key] = remaining - 1
		} else if len(m.appEnvironmentOnGet) > 0 {
			releases := m.appReleases[key]
			m.appEnvironmentOnGet[0](env, &releases)
			m.appReleases[key] = releases
			if len(m.appEnvironmentOnGet) > 1 {
				m.appEnvironmentOnGet = m.appEnvironmentOnGet[1:]
			}
		}
		w.Header().Set("ETag", mockAppEnvironmentETag(env))
		mustEncodeJSON(w, env)
		return
	}
	if r.Method == http.MethodPatch && len(m.appEnvironmentBeforePatch) > 0 {
		m.appEnvironmentBeforePatch[0](env)
		m.appEnvironmentBeforePatch = m.appEnvironmentBeforePatch[1:]
	}
	if r.Header.Get("If-Match") != mockAppEnvironmentETag(env) {
		mockError(w, http.StatusPreconditionFailed, "precondition_failed", "the environment changed")
		return
	}
	if len(parts) == 3 && parts[2] == "retry" && r.Method == http.MethodPost {
		env.Generation++
		env.DeploymentRevision++
		m.appMockRelease(key, env)
		m.appEnvironmentOnGet = m.appEnvironmentAfterWrite
		m.appEnvironmentAfterWrite = nil
		w.Header().Set("ETag", mockAppEnvironmentETag(env))
		w.WriteHeader(http.StatusAccepted)
		mustEncodeJSON(w, client.AppRetryResponse{App: app, Environment: name, Generation: env.Generation, DeploymentRevision: env.DeploymentRevision})
		return
	}
	switch r.Method {
	case http.MethodPatch:
		if m.appsDisabled {
			for key := range body {
				if key != "deletionProtection" {
					mockError(w, http.StatusNotFound, client.CodeAppsUnavailable, "apps are not available on this platform")
					return
				}
			}
		}
		if _, exists := body["deploymentRevision"]; exists {
			mockError(w, http.StatusBadRequest, "", "deploymentRevision cannot be patched")
			return
		}
		priorRuntime, priorSecrets, priorDatabases := env.Runtime, mockReleaseSecrets(env.Runtime, env.Secrets), env.Databases
		data, err := json.Marshal(body)
		if err != nil {
			panic(err)
		}
		var patch client.PatchAppEnvironmentRequest
		if err := json.Unmarshal(data, &patch); err != nil {
			panic(err)
		}
		if patch.Runtime != nil {
			env.Runtime = patch.Runtime
		}
		if patch.Secrets != nil {
			env.Secrets = *patch.Secrets
		}
		if patch.Databases != nil {
			env.Databases = mockDefaultAppDatabases(*patch.Databases)
		}
		if patch.Domains != nil {
			env.Domains = *patch.Domains
		}
		if patch.DeletionProtection != nil {
			env.DeletionProtection = *patch.DeletionProtection
		}
		env.Generation++
		if !reflect.DeepEqual(mockReleaseRuntime(priorRuntime), mockReleaseRuntime(env.Runtime)) || !reflect.DeepEqual(priorSecrets, mockReleaseSecrets(env.Runtime, env.Secrets)) || !reflect.DeepEqual(priorDatabases, env.Databases) {
			m.appMockRelease(key, env)
		} else {
			env.Status.ObservedGeneration = env.Generation
		}
		m.appEnvironmentOnGet = m.appEnvironmentAfterWrite
		m.appEnvironmentAfterWrite = nil
		w.Header().Set("ETag", mockAppEnvironmentETag(env))
		mustEncodeJSON(w, env)
	case http.MethodDelete:
		if body["confirm"] != name {
			mockError(w, http.StatusBadRequest, "confirm_mismatch", "confirmation does not match")
			return
		}
		if env.DeletionProtection {
			mockError(w, http.StatusConflict, client.CodeDeletionProtected, "turn deletion protection off first")
			return
		}
		if m.appEnvironmentNamespaceInUse {
			mockError(w, http.StatusConflict, "namespace_in_use", "an independent secret targets the namespace")
			return
		}
		env.Status.Phase = "Terminating"
		m.appEnvironmentDeleting[key] = m.appEnvironmentDeleteLag
		w.WriteHeader(http.StatusAccepted)
		mustEncodeJSON(w, map[string]any{"status": map[string]any{"phase": "Terminating"}})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func mockAppEnvironmentETag(env *client.AppEnvironment) string {
	return fmt.Sprintf(`"%s.%d"`, env.UID, env.Generation)
}

func (m *mockKupeAPI) appMockRelease(key string, env *client.AppEnvironment) {
	number := int64(len(m.appReleases[key]) + 1)
	name := fmt.Sprintf("%s-%d", env.Name, number)
	phase := m.appEnvironmentOutcome
	if phase == "" {
		phase = "Succeeded"
	}
	runtime := mockReleaseRuntime(env.Runtime)
	release := client.AppRelease{Name: name, Number: number, DeploymentRevision: env.DeploymentRevision, Trigger: "Deploy", Runtime: client.AppReleaseRuntime{AppRuntime: runtime, Secrets: []client.AppReleaseSecret{}}, Status: client.AppReleaseStatus{Phase: phase, Reason: "TestOutcome"}}
	release.Runtime.Secrets = mockReleaseSecrets(env.Runtime, env.Secrets)
	// The runtime is a map keyed by name, so it is not in configured order.
	release.Runtime.Databases = append([]client.AppDatabase{}, env.Databases...)
	sort.Slice(release.Runtime.Databases, func(i, j int) bool { return release.Runtime.Databases[i].Name > release.Runtime.Databases[j].Name })
	env.Status.Databases = []client.AppDatabaseStatus{}
	for _, conn := range env.Databases {
		databasePhase := m.appDatabasePhase
		if databasePhase == "" {
			databasePhase = "Ready"
		}
		env.Status.Databases = append(env.Status.Databases, client.AppDatabaseStatus{Name: conn.Name, Env: conn.Env, Phase: databasePhase, RuntimeSecret: conn.Name + "-app", MigrationSecret: conn.Name + "-migrate"})
	}
	ref := &client.AppReleaseRef{Name: name}
	env.Status.RequestedRelease = ref
	env.Status.Deployment = &client.AppDeployment{Release: ref, Phase: phase, Reason: release.Status.Reason}
	env.Status.ObservedGeneration = env.Generation
	switch phase {
	case "Succeeded":
		env.Status.Phase = "Running"
		env.Status.ActiveRelease = ref
		env.Status.LastSuccessfulRelease = ref
	case "Failed":
		env.Status.Phase = "Degraded"
	default:
		env.Status.Phase = "Upgrading"
	}
	if phase == "Failed" && m.appEnvironmentRecovery {
		previous := &client.AppReleaseRef{Name: "serving-previous"}
		release.Status.Recovery = &client.AppReleaseRecovery{Phase: "Succeeded", Release: previous}
		env.Status.Recovery = &client.AppRecovery{Phase: "Succeeded", Release: previous}
		env.Status.ActiveRelease = previous
	}
	m.appReleases[key] = append([]client.AppRelease{release}, m.appReleases[key]...)
}

// mockReleaseRuntime is a deep copy of runtime as the operator records it in a
// release: under autoscaling, replicas is minPods and a missing cpuPercent is
// the default.
func mockReleaseRuntime(source *client.AppRuntime) client.AppRuntime {
	data, err := json.Marshal(source)
	if err != nil {
		panic(err)
	}
	var runtime client.AppRuntime
	if err := json.Unmarshal(data, &runtime); err != nil {
		panic(err)
	}
	if runtime.Autoscaling != nil {
		if runtime.Autoscaling.CPUPercent == 0 {
			runtime.Autoscaling.CPUPercent = appDefaultCPUPercent
		}
		runtime.Replicas = runtime.Autoscaling.MinPods
	}
	return runtime
}

// mockReleaseSecrets returns the secrets a release records: only those
// runtime.env references. A change to any other secret does not make a release.
func mockReleaseSecrets(runtime *client.AppRuntime, secrets []client.AppSecret) []client.AppReleaseSecret {
	out := []client.AppReleaseSecret{}
	if runtime == nil {
		return out
	}
	for _, secret := range secrets {
		keys := map[string]bool{}
		for _, value := range runtime.Env {
			if value.ValueFrom != nil && value.ValueFrom.SecretKeyRef.Secret == secret.Name {
				keys[value.ValueFrom.SecretKeyRef.Key] = true
			}
		}
		if len(keys) > 0 {
			names := []string{}
			for key := range keys {
				names = append(names, key)
			}
			sort.Strings(names)
			out = append(out, client.AppReleaseSecret{Name: secret.Name, Path: secret.Path, Keys: names})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// mockDefaultAppDatabases gives a connection without env DATABASE_URL, as the
// API does, and stores an absent list as none.
func mockDefaultAppDatabases(databases []client.AppDatabase) []client.AppDatabase {
	out := []client.AppDatabase{}
	for _, conn := range databases {
		if conn.Env == "" {
			conn.Env = defaultAppDatabaseEnv
		}
		out = append(out, conn)
	}
	return out
}
