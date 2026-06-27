package telosd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/telos-org/telos/internal/sessionapi"
)

func TestKubernetesSubstrateAppliesControllerWorker(t *testing.T) {
	setLiteLLMGatewayEnv(t)

	cfg := testCloudConfig(t)
	client := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: cfg.Kubernetes.EnvNamespace}},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "telos-env-keys", Namespace: cfg.Kubernetes.EnvNamespace},
			Type:       corev1.SecretTypeOpaque,
			Data:       map[string][]byte{"TELOS_LITELLM_BASE_URL": []byte("https://stored-litellm.example.com/v1")},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: cfg.Kubernetes.AgentSecretName, Namespace: cfg.Kubernetes.EnvNamespace},
			Type:       corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"TELOS_LITELLM_API_KEY":  []byte("stored-litellm-key"),
				"TELOS_LITELLM_BASE_URL": []byte("https://stored-litellm.example.com/v1"),
			},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: cfg.Kubernetes.ImagePullSecret, Namespace: cfg.Kubernetes.EnvNamespace},
			Type:       corev1.SecretTypeDockerConfigJson,
			Data:       map[string][]byte{corev1.DockerConfigJsonKey: []byte("{}")},
		},
	)
	substrate := newKubernetesSubstrateWithClient(cfg, client)
	session := testCloudSession(t, sessionapi.KindController)

	if err := substrate.Apply(session, "controller_started", ""); err != nil {
		t.Fatal(err)
	}

	namespace := workerNamespace(session.SessionID, sessionapi.KindController)
	name := workerWorkloadName(session.SessionID, sessionapi.KindController)
	deployment, err := client.AppsV1().Deployments(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	assertWorkerTemplate(t, &deployment.Spec.Template, session.SessionID, "controller_started")
	if len(deployment.Spec.Template.Spec.InitContainers) != 1 {
		t.Fatalf("init containers: got %d", len(deployment.Spec.Template.Spec.InitContainers))
	}
	installCommand := strings.Join(deployment.Spec.Template.Spec.InitContainers[0].Command, " ")
	if !strings.Contains(installCommand, "https://usetelos.ai/releases") {
		t.Fatalf("install command missing public release URL: %q", installCommand)
	}
	if len(deployment.Spec.Template.Spec.ImagePullSecrets) != 1 ||
		deployment.Spec.Template.Spec.ImagePullSecrets[0].Name != cfg.Kubernetes.ImagePullSecret {
		t.Fatalf("image pull secrets: got %+v", deployment.Spec.Template.Spec.ImagePullSecrets)
	}

	assertSecretExists(t, client, namespace, cfg.Kubernetes.AgentSecretName)
	assertSecretData(t, client, namespace, cfg.Kubernetes.AgentSecretName, "TELOS_LITELLM_API_KEY", "test-litellm-key")
	assertSecretData(t, client, namespace, cfg.Kubernetes.AgentSecretName, "TELOS_LITELLM_BASE_URL", "https://litellm.example.com/v1")
	assertSecretExists(t, client, namespace, "telos-env-keys")
	assertSecretExists(t, client, namespace, cfg.Kubernetes.ImagePullSecret)
	role, err := client.RbacV1().ClusterRoles().Get(context.Background(), workerClusterRole(namespace).Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	assertNoWorkerRBACEscalation(t, role.Rules)
}

func TestKubernetesSubstrateAppliesTaskWorker(t *testing.T) {
	setLiteLLMGatewayEnv(t)

	cfg := testCloudConfig(t)
	client := fake.NewSimpleClientset(testEnvObjects(cfg)...)
	substrate := newKubernetesSubstrateWithClient(cfg, client)
	session := testCloudSession(t, sessionapi.KindTask)

	if err := substrate.Apply(session, "task_started", ""); err != nil {
		t.Fatal(err)
	}

	namespace := workerNamespace(session.SessionID, sessionapi.KindTask)
	name := workerWorkloadName(session.SessionID, sessionapi.KindTask)
	job, err := client.BatchV1().Jobs(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	assertWorkerTemplate(t, &job.Spec.Template, session.SessionID, "task_started")
	if job.Spec.Template.Spec.RestartPolicy != corev1.RestartPolicyNever {
		t.Fatalf("restart policy: got %q", job.Spec.Template.Spec.RestartPolicy)
	}
}

func TestKubernetesSubstrateStopDeletesWorkerResources(t *testing.T) {
	setLiteLLMGatewayEnv(t)

	cfg := testCloudConfig(t)
	client := fake.NewSimpleClientset(testEnvObjects(cfg)...)
	substrate := newKubernetesSubstrateWithClient(cfg, client)
	session := testCloudSession(t, sessionapi.KindController)
	namespace := workerNamespace(session.SessionID, sessionapi.KindController)
	name := workerWorkloadName(session.SessionID, sessionapi.KindController)

	if err := substrate.Apply(session, "controller_started", ""); err != nil {
		t.Fatal(err)
	}
	if err := substrate.Stop(session); err != nil {
		t.Fatal(err)
	}

	if _, err := client.AppsV1().Deployments(namespace).Get(context.Background(), name, metav1.GetOptions{}); err == nil {
		t.Fatalf("deployment %s still exists", name)
	}
	if _, err := client.CoreV1().Namespaces().Get(context.Background(), namespace, metav1.GetOptions{}); err == nil {
		t.Fatalf("namespace %s still exists", namespace)
	}
	if _, err := client.RbacV1().ClusterRoles().Get(context.Background(), workerClusterRole(namespace).Name, metav1.GetOptions{}); err == nil {
		t.Fatalf("clusterrole for %s still exists", namespace)
	}
	if _, err := client.RbacV1().ClusterRoleBindings().Get(context.Background(), workerClusterRoleBinding(namespace).Name, metav1.GetOptions{}); err == nil {
		t.Fatalf("clusterrolebinding for %s still exists", namespace)
	}
}

func TestKubernetesSubstrateStopContinuesCleanupAfterWorkloadDeleteError(t *testing.T) {
	setLiteLLMGatewayEnv(t)

	cfg := testCloudConfig(t)
	client := fake.NewSimpleClientset(testEnvObjects(cfg)...)
	substrate := newKubernetesSubstrateWithClient(cfg, client)
	session := testCloudSession(t, sessionapi.KindController)
	namespace := workerNamespace(session.SessionID, sessionapi.KindController)

	if err := substrate.Apply(session, "controller_started", ""); err != nil {
		t.Fatal(err)
	}
	client.Fake.PrependReactor("delete", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("deployment delete unavailable")
	})

	err := substrate.Stop(session)
	if err == nil {
		t.Fatal("expected workload delete error")
	}

	if _, err := client.CoreV1().Namespaces().Get(context.Background(), namespace, metav1.GetOptions{}); err == nil {
		t.Fatalf("namespace %s still exists", namespace)
	}
	if _, err := client.RbacV1().ClusterRoles().Get(context.Background(), workerClusterRole(namespace).Name, metav1.GetOptions{}); err == nil {
		t.Fatalf("clusterrole for %s still exists", namespace)
	}
	if _, err := client.RbacV1().ClusterRoleBindings().Get(context.Background(), workerClusterRoleBinding(namespace).Name, metav1.GetOptions{}); err == nil {
		t.Fatalf("clusterrolebinding for %s still exists", namespace)
	}
}

func TestCloudSessionStoreCleansKubernetesResourcesWhenInitialApplyFails(t *testing.T) {
	setLiteLLMGatewayEnv(t)

	cfg := testCloudConfig(t)
	client := fake.NewSimpleClientset(testEnvObjects(cfg)...)
	client.Fake.PrependReactor("create", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("deployment create unavailable")
	})
	substrate := newKubernetesSubstrateWithClient(cfg, client)
	base := sessionapi.NewFileStore(t.TempDir(), sessionapi.RuntimeCloud)
	store := newCloudSessionStore(base, routeHandleResolver{}, substrate)
	markdown := "---\nversion: v0\nname: postgres\nplatform: cloud\n---\n# Postgres\n"

	_, err := store.Create(sessionapi.SessionCreateRequest{SpecMarkdown: &markdown})
	if err == nil {
		t.Fatal("expected worker launch error")
	}
	sessions, err := base.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Fatalf("orphan sessions: got %+v", sessions)
	}
	assertNoWorkerResources(t, client)
}

func TestKubernetesSubstrateAgentSecretAcceptsLiteLLMBaseURLAlias(t *testing.T) {
	t.Setenv("TELOS_LITELLM_API_KEY", "test-litellm-key")
	t.Setenv("TELOS_LITELLM_BASE_URL", "")
	t.Setenv("TELOS_API_BASE_URL", "")
	t.Setenv("TELOS_BASE_URL", "")

	cfg := testCloudConfig(t)
	targetNamespace := "ns-worker"
	client := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: cfg.Kubernetes.EnvNamespace}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: targetNamespace}},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: cfg.Kubernetes.AgentSecretName, Namespace: cfg.Kubernetes.EnvNamespace},
			Type:       corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"TELOS_LITELLM_API_KEY": []byte("stored-litellm-key"),
				"TELOS_API_BASE_URL":    []byte("https://alias-litellm.example.com/v1"),
			},
		},
	)
	substrate := newKubernetesSubstrateWithClient(cfg, client)

	if err := substrate.createOrUpdateAgentSecret(context.Background(), targetNamespace, nil); err != nil {
		t.Fatal(err)
	}

	assertSecretData(t, client, targetNamespace, cfg.Kubernetes.AgentSecretName, "TELOS_LITELLM_API_KEY", "test-litellm-key")
	assertSecretData(t, client, targetNamespace, cfg.Kubernetes.AgentSecretName, "TELOS_API_BASE_URL", "https://alias-litellm.example.com/v1")
}

func TestKubernetesSubstrateAgentSecretRequiresLiteLLMBaseURL(t *testing.T) {
	t.Setenv("TELOS_LITELLM_API_KEY", "test-litellm-key")
	t.Setenv("TELOS_LITELLM_BASE_URL", "")
	t.Setenv("TELOS_API_BASE_URL", "")
	t.Setenv("TELOS_BASE_URL", "")

	cfg := testCloudConfig(t)
	targetNamespace := "ns-worker"
	client := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: cfg.Kubernetes.EnvNamespace}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: targetNamespace}},
	)
	substrate := newKubernetesSubstrateWithClient(cfg, client)

	err := substrate.createOrUpdateAgentSecret(context.Background(), targetNamespace, nil)
	if err == nil || !strings.Contains(err.Error(), "TELOS_LITELLM_BASE_URL is required") {
		t.Fatalf("expected missing LiteLLM base URL error, got %v", err)
	}
}

func TestKubernetesSubstrateMintsAndReusesSessionGatewaySecret(t *testing.T) {
	t.Setenv("TELOS_LITELLM_API_KEY", "")
	t.Setenv("TELOS_LITELLM_BASE_URL", "")
	mintCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/internal/sessions/sess_cloud/mint" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer billing-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("X-Telos-User-Authorization") != "Bearer user-token" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		mintCalls++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"session_id": "sess_cloud",
			"base_url":   "https://managed.example.com/v1",
			"api_key":    "sk-managed",
			"key_alias":  "sess_cloud",
		})
	}))
	defer server.Close()

	cfg := testCloudConfig(t)
	cfg.Billing.Endpoint = server.URL
	cfg.Billing.EnvID = "env_test"
	cfg.Billing.Token = "billing-token"
	client := fake.NewSimpleClientset(testEnvObjects(cfg)...)
	substrate := newKubernetesSubstrateWithClient(cfg, client)

	cred, err := substrate.sessionGatewayCredential(context.Background(), "sess_cloud", "", "Bearer user-token")
	if err != nil {
		t.Fatal(err)
	}
	if cred.APIKey != "sk-managed" || cred.BaseURL != "https://managed.example.com/v1" {
		t.Fatalf("credential: %+v", cred)
	}
	if mintCalls != 1 {
		t.Fatalf("mint calls: got %d", mintCalls)
	}
	cred, err = substrate.sessionGatewayCredential(context.Background(), "sess_cloud", "", "Bearer user-token")
	if err != nil {
		t.Fatal(err)
	}
	if mintCalls != 1 {
		t.Fatalf("expected persisted credential reuse, mint calls: got %d", mintCalls)
	}

	targetNamespace := "ns-worker"
	_, err = client.CoreV1().Namespaces().Create(context.Background(), &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: targetNamespace},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := substrate.createOrUpdateAgentSecret(context.Background(), targetNamespace, cred); err != nil {
		t.Fatal(err)
	}
	assertSecretData(t, client, targetNamespace, cfg.Kubernetes.AgentSecretName, "TELOS_LITELLM_API_KEY", "sk-managed")
	assertSecretData(t, client, targetNamespace, cfg.Kubernetes.AgentSecretName, "TELOS_LITELLM_BASE_URL", "https://managed.example.com/v1")
}

func TestBillingClientMintsChildSessionWithParentLineage(t *testing.T) {
	var gotBody map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/internal/sessions/sess_child/mint" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer billing-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("X-Telos-User-Authorization") != "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"session_id": "sess_child",
			"base_url":   "https://managed.example.com/v1",
			"api_key":    "sk-child",
			"key_alias":  "sess_child",
		})
	}))
	defer server.Close()

	client := newBillingClient(BillingConfig{
		Endpoint: server.URL,
		EnvID:    "env_test",
		Token:    "billing-token",
	})
	cred, err := client.MintSessionKey("sess_child", "sess_parent", "")
	if err != nil {
		t.Fatal(err)
	}
	if cred.APIKey != "sk-child" {
		t.Fatalf("credential: %+v", cred)
	}
	if gotBody["env_id"] != "env_test" || gotBody["parent_session_id"] != "sess_parent" {
		t.Fatalf("body: %+v", gotBody)
	}
}

func TestBillingClientReconcilesTerminalSession(t *testing.T) {
	gotRequest := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/billing/reconcile/sess_cloud" || r.URL.RawQuery != "terminal=true" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer billing-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		gotRequest = true
		_ = json.NewEncoder(w).Encode(map[string]any{
			"session_id":    "sess_cloud",
			"spent_usd":     0.12,
			"units_debited": 12,
			"state":         "settled",
		})
	}))
	defer server.Close()

	client := newBillingClient(BillingConfig{
		Endpoint: server.URL,
		EnvID:    "env_test",
		Token:    "billing-token",
	})
	if err := client.ReconcileSession("sess_cloud", true); err != nil {
		t.Fatal(err)
	}
	if !gotRequest {
		t.Fatal("missing reconcile request")
	}
}

func testCloudConfig(t *testing.T) Config {
	t.Helper()
	cfg, err := NormalizeConfig(Config{
		Mode: ModeCloud,
		Auth: AuthConfig{Token: "operator-token"},
		Kubernetes: KubernetesConfig{
			AgentImage:      "telos-agent:test",
			EnvNamespace:    "ns-telos-env",
			ImagePullSecret: "gar-pull",
			CopySecrets:     []string{"telos-env-keys"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func setLiteLLMGatewayEnv(t *testing.T) {
	t.Helper()
	t.Setenv("TELOS_LITELLM_API_KEY", "test-litellm-key")
	t.Setenv("TELOS_LITELLM_BASE_URL", "https://litellm.example.com/v1")
}

func testEnvObjects(cfg Config) []runtime.Object {
	return []runtime.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: cfg.Kubernetes.EnvNamespace}},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "telos-env-keys", Namespace: cfg.Kubernetes.EnvNamespace},
			Type:       corev1.SecretTypeOpaque,
			Data:       map[string][]byte{"TELOS_LITELLM_BASE_URL": []byte("https://stored-litellm.example.com/v1")},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: cfg.Kubernetes.ImagePullSecret, Namespace: cfg.Kubernetes.EnvNamespace},
			Type:       corev1.SecretTypeDockerConfigJson,
			Data:       map[string][]byte{corev1.DockerConfigJsonKey: []byte("{}")},
		},
	}
}

func testCloudSession(t *testing.T, kind sessionapi.SessionKind) *sessionapi.Session {
	t.Helper()
	store := sessionapi.NewFileStore(t.TempDir(), sessionapi.RuntimeCloud)
	markdown := "---\nversion: v0\nname: postgres\nplatform: cloud\n---\n# Postgres\n"
	req := sessionapi.SessionCreateRequest{SpecMarkdown: &markdown}
	if kind == sessionapi.KindTask {
		parent := "sess_20260518_000000_parent"
		req.ParentSessionID = &parent
	}
	session, err := store.Create(req)
	if err != nil {
		t.Fatal(err)
	}
	if session.SessionKind == nil || *session.SessionKind != kind {
		t.Fatalf("session kind: got %#v", session.SessionKind)
	}
	return session
}

func assertWorkerTemplate(t *testing.T, template *corev1.PodTemplateSpec, sessionID string, wakeReason string) {
	t.Helper()
	if template.Labels["telos/session"] != sessionID {
		t.Fatalf("session label: got %q", template.Labels["telos/session"])
	}
	if template.Annotations["telos.ai/wake-reason"] != wakeReason {
		t.Fatalf("wake reason: got %q", template.Annotations["telos.ai/wake-reason"])
	}
	if template.Annotations["telos.ai/runtime-version"] != "latest" {
		t.Fatalf("runtime version: got %q", template.Annotations["telos.ai/runtime-version"])
	}
	if len(template.Spec.Containers) != 1 {
		t.Fatalf("containers: got %d", len(template.Spec.Containers))
	}
	if len(template.Spec.InitContainers) != 1 {
		t.Fatalf("init containers: got %d", len(template.Spec.InitContainers))
	}
	if template.Spec.SecurityContext == nil ||
		template.Spec.SecurityContext.FSGroup == nil ||
		*template.Spec.SecurityContext.FSGroup != 1000 {
		t.Fatalf("pod security context: got %+v", template.Spec.SecurityContext)
	}
	assertAgentSecurityContext(t, template.Spec.InitContainers[0].SecurityContext)
	initContainer := template.Spec.InitContainers[0]
	assertNoLegacyAgentConfig(t, template.Spec.Volumes, initContainer.VolumeMounts)
	container := template.Spec.Containers[0]
	assertAgentSecurityContext(t, container.SecurityContext)
	if len(container.EnvFrom) != 1 ||
		container.EnvFrom[0].SecretRef == nil ||
		container.EnvFrom[0].SecretRef.Name != "agent-api-keys" {
		t.Fatalf("worker envFrom: got %+v", container.EnvFrom)
	}
	if len(container.Command) != 3 ||
		container.Command[0] != "/telos-runtime/telosd" ||
		container.Command[1] != "--session-dir" {
		t.Fatalf("worker command: got %+v", container.Command)
	}
	if container.Command[2] != "/telos-state/sessions/"+sessionID {
		t.Fatalf("session dir: got %+v", container.Command)
	}
	assertNoLegacyAgentConfig(t, template.Spec.Volumes, container.VolumeMounts)
}

func assertNoLegacyAgentConfig(t *testing.T, volumes []corev1.Volume, mounts []corev1.VolumeMount) {
	t.Helper()
	legacyToken := "p" + "i"
	legacyHome := "/home/agent/.p" + "i/agent"
	legacySeed := "telos-" + "pi"
	for _, volume := range volumes {
		if strings.Contains(volume.Name, legacyToken) {
			t.Fatalf("worker template should not include legacy agent config volumes: %+v", volumes)
		}
	}
	for _, mount := range mounts {
		if strings.Contains(mount.Name, legacyToken) || strings.Contains(mount.MountPath, legacyHome) || strings.Contains(mount.MountPath, legacySeed) {
			t.Fatalf("worker template should not include legacy agent config mounts: %+v", mounts)
		}
	}
}

func assertAgentSecurityContext(t *testing.T, ctx *corev1.SecurityContext) {
	t.Helper()
	if ctx == nil {
		t.Fatal("missing agent security context")
	}
	if ctx.RunAsUser == nil || *ctx.RunAsUser == 0 {
		t.Fatalf("agent runs as root: %+v", ctx)
	}
	if ctx.RunAsGroup == nil || *ctx.RunAsGroup == 0 {
		t.Fatalf("agent group is root: %+v", ctx)
	}
	if ctx.RunAsNonRoot == nil || !*ctx.RunAsNonRoot {
		t.Fatalf("agent does not require non-root: %+v", ctx)
	}
	if ctx.AllowPrivilegeEscalation == nil || *ctx.AllowPrivilegeEscalation {
		t.Fatalf("agent allows privilege escalation: %+v", ctx)
	}
}

func assertSecretExists(t *testing.T, client *fake.Clientset, namespace string, name string) {
	t.Helper()
	if _, err := client.CoreV1().Secrets(namespace).Get(context.Background(), name, metav1.GetOptions{}); err != nil {
		t.Fatal(err)
	}
}

func assertSecretData(t *testing.T, client *fake.Clientset, namespace string, name string, key string, want string) {
	t.Helper()
	secret, err := client.CoreV1().Secrets(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(secret.Data[key]); got != want {
		t.Fatalf("%s/%s[%s]: got %q, want %q", namespace, name, key, got, want)
	}
}

func assertNoWorkerRBACEscalation(t *testing.T, rules []rbacv1.PolicyRule) {
	t.Helper()
	writeVerbs := map[string]bool{"create": true, "update": true, "patch": true, "delete": true}
	for _, rule := range rules {
		if !hasWriteVerb(rule.Verbs, writeVerbs) {
			continue
		}
		if contains(rule.APIGroups, "rbac.authorization.k8s.io") {
			t.Fatalf("worker role can write RBAC resources: %+v", rule)
		}
		if contains(rule.APIGroups, "") && contains(rule.Resources, "serviceaccounts") {
			t.Fatalf("worker role can write serviceaccounts: %+v", rule)
		}
	}
}

func assertNoWorkerResources(t *testing.T, client *fake.Clientset) {
	t.Helper()
	namespaces, err := client.CoreV1().Namespaces().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, namespace := range namespaces.Items {
		if strings.HasPrefix(namespace.Name, "ns-ctrl-") || strings.HasPrefix(namespace.Name, "ns-task-") {
			t.Fatalf("orphan worker namespace: %s", namespace.Name)
		}
	}
	clusterRoles, err := client.RbacV1().ClusterRoles().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range clusterRoles.Items {
		if strings.HasPrefix(role.Name, "agent-worker-ns-") {
			t.Fatalf("orphan worker clusterrole: %s", role.Name)
		}
	}
	clusterRoleBindings, err := client.RbacV1().ClusterRoleBindings().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, binding := range clusterRoleBindings.Items {
		if strings.HasPrefix(binding.Name, "agent-worker-ns-") {
			t.Fatalf("orphan worker clusterrolebinding: %s", binding.Name)
		}
	}
}

func hasWriteVerb(verbs []string, writeVerbs map[string]bool) bool {
	for _, verb := range verbs {
		if writeVerbs[verb] {
			return true
		}
	}
	return false
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
