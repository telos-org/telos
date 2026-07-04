package telosd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/telos-org/telos/internal/gatewaycred"
	"github.com/telos-org/telos/internal/sessionapi"
)

type kubernetesSubstrate struct {
	client  kubernetes.Interface
	billing *billingClient

	agentImage         string
	envNamespace       string
	stateMountRoot     string
	stateHostRoot      string
	stateNodeRoot      string
	stateStorageClass  string
	stateStorageSize   string
	allowHostPathState bool
	workerEgressCIDRs  []string
	imagePullSecret    string
	agentSecretName    string
	agentSecretKey     string
	copySecrets        []string
	runtimeBaseURL     string
	runtimeVersion     string
	runtimeArtifacts   map[string]string
	runtimeMountPath   string
	runtimeTelosPath   string
	runtimeTelosdPath  string
	billingEndpoint    string
	billingEnvID       string
	billingToken       string
	billingTokenFile   string
}

var sessionGatewayLocks sync.Map

var workerNamespaceLabels = map[string]string{
	"pod-security.kubernetes.io/enforce": "privileged",
	"pod-security.kubernetes.io/audit":   "privileged",
	"pod-security.kubernetes.io/warn":    "privileged",
}

const (
	gatewayAPIKeyEnv    = "TELOS_GATEWAY_API_KEY"
	gatewayBaseURLEnv   = "TELOS_GATEWAY_BASE_URL"
	gatewayTransportEnv = "TELOS_GATEWAY_TRANSPORT"
	gatewayKindEnv      = "TELOS_GATEWAY_KIND"
	gatewayHeadersEnv   = "TELOS_GATEWAY_HEADERS"
	gatewayKeyAliasEnv  = "TELOS_GATEWAY_KEY_ALIAS"
	modelProfileEnv     = "TELOS_MODEL_PROFILE"
	gatewayModeEnv      = "TELOS_GATEWAY_MODE"
)

const sessionAPITokenSecretKey = "token"

var gatewayEnvNames = []string{
	gatewayBaseURLEnv,
	gatewayTransportEnv,
	gatewayKindEnv,
	gatewayHeadersEnv,
	modelProfileEnv,
	gatewayModeEnv,
}

var directProviderKeyNames = []string{
	"ANTHROPIC_API_KEY",
	"OPENAI_API_KEY",
	"SAIL_API_KEY",
	"SILARES_API_KEY",
}

var legacyGatewayEnvNames = []string{
	"TELOS_LITELLM_BASE_URL",
	"TELOS_LITELLM_API_KEY",
	"TELOS_LITELLM_KEY_ALIAS",
	"TELOS_API_BASE_URL",
	"TELOS_BASE_URL",
	"TELOS_API_KEY",
}

var optionalGatewayCredentialEnvNames = []string{
	gatewayTransportEnv,
	gatewayKindEnv,
	gatewayHeadersEnv,
	gatewayKeyAliasEnv,
	modelProfileEnv,
	gatewayModeEnv,
}

func newKubernetesSubstrate(cfg Config) (kubernetesSubstrate, error) {
	restCfg, err := kubernetesRESTConfig()
	if err != nil {
		return kubernetesSubstrate{}, err
	}
	client, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return kubernetesSubstrate{}, err
	}
	return newKubernetesSubstrateWithClient(cfg, client), nil
}

func newKubernetesSubstrateWithClient(cfg Config, client kubernetes.Interface) kubernetesSubstrate {
	runtimeMountPath := cfg.Runtime.MountPath
	return kubernetesSubstrate{
		client:             client,
		billing:            newBillingClient(cfg.Billing),
		agentImage:         cfg.Kubernetes.AgentImage,
		envNamespace:       cfg.Kubernetes.EnvNamespace,
		stateMountRoot:     cfg.Kubernetes.StateMountRoot,
		stateHostRoot:      cfg.Kubernetes.StateHostRoot,
		stateNodeRoot:      cfg.Kubernetes.StateNodeRoot,
		stateStorageClass:  cfg.Kubernetes.StateStorageClass,
		stateStorageSize:   cfg.Kubernetes.StateStorageSize,
		allowHostPathState: cfg.Kubernetes.AllowHostPathState,
		workerEgressCIDRs:  append([]string{}, cfg.Kubernetes.WorkerEgressCIDRs...),
		imagePullSecret:    cfg.Kubernetes.ImagePullSecret,
		agentSecretName:    cfg.Kubernetes.AgentSecretName,
		agentSecretKey:     cfg.Kubernetes.AgentSecretKey,
		copySecrets:        append([]string{}, cfg.Kubernetes.CopySecrets...),
		runtimeBaseURL:     cfg.Runtime.ArtifactBaseURL,
		runtimeVersion:     cfg.Runtime.ArtifactVersion,
		runtimeArtifacts:   runtimeArtifactDigestMap(cfg.Runtime.Artifacts),
		runtimeMountPath:   runtimeMountPath,
		runtimeTelosPath:   runtimeMountPath + "/telos",
		runtimeTelosdPath:  runtimeMountPath + "/telosd",
		billingEndpoint:    cfg.Billing.Endpoint,
		billingEnvID:       cfg.Billing.EnvID,
		billingToken:       cfg.Billing.Token,
		billingTokenFile:   cfg.Billing.TokenFile,
	}
}

func kubernetesRESTConfig() (*rest.Config, error) {
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		return rest.InClusterConfig()
	}
	kubeconfig := strings.TrimSpace(os.Getenv("KUBECONFIG"))
	if kubeconfig == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		kubeconfig = filepath.Join(home, ".kube", "config")
	}
	return clientcmd.BuildConfigFromFlags("", kubeconfig)
}

func (s kubernetesSubstrate) Apply(session *sessionapi.Session, wakeReason string, userAuthorization string, userOrgID string) error {
	kind, err := sessionWorkerKind(session)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	m, err := sessionapi.ReadManifest(filepath.Join(ptrValue(session.SessionDir), "session.json"))
	if err != nil {
		return fmt.Errorf("read worker manifest: %w", err)
	}
	modelProfile, err := sessionapi.NormalizeModelProfile(string(m.Config.ModelProfile))
	if err != nil {
		return err
	}
	credential, err := s.sessionGatewayCredential(ctx, session.SessionID, ptrValue(m.ParentSessionID), userAuthorization, userOrgID, modelProfile)
	if err != nil {
		return err
	}
	namespace := workerNamespace(session.SessionID, kind)
	if err := s.prepareWorkerNamespace(ctx, namespace, credential); err != nil {
		return err
	}
	tokenSecretName, err := s.recreateSessionAPITokenSecret(ctx, namespace, session, kind, m)
	if err != nil {
		return err
	}
	switch kind {
	case sessionapi.KindController:
		return s.createOrUpdateDeployment(ctx, s.controllerDeployment(session.SessionID, m, wakeReason, tokenSecretName))
	case sessionapi.KindTask:
		return s.createJobIfMissing(ctx, s.taskJob(session.SessionID, m, wakeReason, tokenSecretName))
	default:
		return fmt.Errorf("invalid session_kind %q", kind)
	}
}

func (s kubernetesSubstrate) Stop(session *sessionapi.Session) error {
	kind, err := sessionWorkerKind(session)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	name := workerWorkloadName(session.SessionID, kind)
	namespace := workerNamespace(session.SessionID, kind)
	propagation := metav1.DeletePropagationForeground
	opts := metav1.DeleteOptions{PropagationPolicy: &propagation}
	if kind == sessionapi.KindController {
		err = s.client.AppsV1().Deployments(namespace).Delete(ctx, name, opts)
	} else {
		err = s.client.BatchV1().Jobs(namespace).Delete(ctx, name, opts)
	}
	var stopErr error
	if err != nil && !apierrors.IsNotFound(err) {
		stopErr = errors.Join(stopErr, fmt.Errorf("delete worker workload %s/%s: %w", namespace, name, err))
	}
	if err := s.deleteNamespace(ctx, namespace); err != nil {
		stopErr = errors.Join(stopErr, fmt.Errorf("delete worker namespace %s: %w", namespace, err))
	}
	if err := s.deleteClusterRoleBinding(ctx, workerClusterRoleBinding(namespace).Name); err != nil {
		stopErr = errors.Join(stopErr, fmt.Errorf("delete worker clusterrolebinding %s: %w", workerClusterRoleBinding(namespace).Name, err))
	}
	if err := s.deleteClusterRole(ctx, workerClusterRole(namespace).Name); err != nil {
		stopErr = errors.Join(stopErr, fmt.Errorf("delete worker clusterrole %s: %w", workerClusterRole(namespace).Name, err))
	}
	if err := s.reconcileManagedBilling(ctx, session.SessionID, true); err != nil {
		stopErr = errors.Join(stopErr, err)
	}
	return stopErr
}

func (s kubernetesSubstrate) RuntimeStatus(session *sessionapi.Session) (sessionapi.SessionStatus, error) {
	kind, err := sessionWorkerKind(session)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	name := workerWorkloadName(session.SessionID, kind)
	namespace := workerNamespace(session.SessionID, kind)
	switch kind {
	case sessionapi.KindController:
		deployment, err := s.client.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			status := sessionapi.StatusStale
			s.reconcileObservedStatus(ctx, session.SessionID, status)
			return status, nil
		}
		if err != nil {
			return "", err
		}
		if deploymentProgressDeadlineExceeded(deployment) {
			status := sessionapi.StatusStale
			s.reconcileObservedStatus(ctx, session.SessionID, status)
			return status, nil
		}
		status := sessionapi.StatusRunning
		s.reconcileObservedStatus(ctx, session.SessionID, status)
		return status, nil
	case sessionapi.KindTask:
		job, err := s.client.BatchV1().Jobs(namespace).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			status := sessionapi.StatusStale
			s.reconcileObservedStatus(ctx, session.SessionID, status)
			return status, nil
		}
		if err != nil {
			return "", err
		}
		if jobConditionTrue(job, batchv1.JobFailed) || job.Status.Failed > 0 {
			status := sessionapi.StatusFailed
			s.reconcileObservedStatus(ctx, session.SessionID, status)
			return status, nil
		}
		if jobConditionTrue(job, batchv1.JobComplete) || job.Status.Succeeded > 0 {
			status := sessionapi.StatusStale
			s.reconcileObservedStatus(ctx, session.SessionID, status)
			return status, nil
		}
		status := sessionapi.StatusRunning
		s.reconcileObservedStatus(ctx, session.SessionID, status)
		return status, nil
	default:
		return "", fmt.Errorf("invalid session_kind %q", kind)
	}
}

func (s kubernetesSubstrate) reconcileObservedStatus(ctx context.Context, sessionID string, status sessionapi.SessionStatus) {
	if status == "" {
		return
	}
	if err := s.reconcileManagedBilling(ctx, sessionID, status.IsTerminal()); err != nil {
		fmt.Fprintf(os.Stderr, "warning: reconcile managed billing: %v\n", err)
	}
}

func (s kubernetesSubstrate) reconcileManagedBilling(ctx context.Context, sessionID string, terminal bool) error {
	if !s.managedGatewayEnabled() || s.billing == nil || !s.billing.configured() {
		return nil
	}
	secretName := sessionGatewaySecretName(sessionID)
	if _, err := s.client.CoreV1().Secrets(s.envNamespace).Get(ctx, secretName, metav1.GetOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("read session gateway secret %s/%s: %w", s.envNamespace, secretName, err)
	}
	if err := s.billing.ReconcileSession(sessionID, terminal); err != nil {
		fmt.Fprintf(os.Stderr, "warning: reconcile managed billing: %v\n", err)
		return nil
	}
	if terminal {
		if err := s.deleteSecret(ctx, s.envNamespace, secretName); err != nil {
			return fmt.Errorf("delete session gateway secret %s/%s: %w", s.envNamespace, secretName, err)
		}
	}
	return nil
}

func deploymentProgressDeadlineExceeded(deployment *appsv1.Deployment) bool {
	if deployment == nil {
		return false
	}
	for _, condition := range deployment.Status.Conditions {
		if condition.Type == appsv1.DeploymentProgressing &&
			condition.Status == corev1.ConditionFalse &&
			condition.Reason == "ProgressDeadlineExceeded" {
			return true
		}
	}
	return false
}

func jobConditionTrue(job *batchv1.Job, conditionType batchv1.JobConditionType) bool {
	if job == nil {
		return false
	}
	for _, condition := range job.Status.Conditions {
		if condition.Type == conditionType && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func (s kubernetesSubstrate) prepareWorkerNamespace(ctx context.Context, namespace string, credential *controlSessionKey) error {
	if s.stateStorageClass == "" && !s.allowHostPathState {
		return fmt.Errorf("kubernetes.allow_host_path_state must be true when kubernetes.state_storage_class is not configured")
	}
	if err := s.createNamespaceIfMissing(ctx, namespace); err != nil {
		return err
	}
	if err := s.createOrUpdateServiceAccount(ctx, serviceAccount(namespace)); err != nil {
		return err
	}
	if err := s.createOrUpdateClusterRole(ctx, workerClusterRole(namespace)); err != nil {
		return err
	}
	if err := s.createOrUpdateClusterRoleBinding(ctx, workerClusterRoleBinding(namespace)); err != nil {
		return err
	}
	if s.stateStorageClass != "" {
		if err := s.createOrUpdatePersistentVolumeClaim(ctx, s.statePersistentVolumeClaim(namespace)); err != nil {
			return err
		}
	}
	for _, policy := range workerNetworkPolicies(namespace, s.workerEgressCIDRs) {
		if err := s.createOrUpdateNetworkPolicy(ctx, policy); err != nil {
			return err
		}
	}
	if err := s.createOrUpdateAgentSecret(ctx, namespace, credential); err != nil {
		return err
	}
	for _, name := range append([]string{}, s.copySecrets...) {
		if err := s.copySecret(ctx, s.envNamespace, namespace, name); err != nil {
			return err
		}
	}
	if s.imagePullSecret != "" {
		if err := s.copySecret(ctx, s.envNamespace, namespace, s.imagePullSecret); err != nil {
			return err
		}
	}
	return nil
}

func (s kubernetesSubstrate) agentSecret(namespace string, credential *controlSessionKey) *corev1.Secret {
	data := map[string][]byte{}
	applyConfiguredGatewayAPIKey(data, s.agentSecretKey)
	applyConfiguredGatewayEnv(data)
	s.applyBillingCredential(data)
	applyGatewayCredential(data, s.agentSecretKey, credential)
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      s.agentSecretName,
			Namespace: namespace,
			Labels: map[string]string{
				"telos/role": "worker-agent",
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: data,
	}
}

func (s kubernetesSubstrate) controllerDeployment(
	sessionID string,
	m *sessionapi.Manifest,
	wakeReason string,
	tokenSecretName string,
) *appsv1.Deployment {
	name := workerWorkloadName(sessionID, sessionapi.KindController)
	namespace := workerNamespace(sessionID, sessionapi.KindController)
	labels := workerLabels(name, sessionID, sessionapi.KindController, ptrValue(m.ParentSessionID))
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   namespace,
			Labels:      labels,
			Annotations: s.workerAnnotations(m, wakeReason),
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(1),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app.kubernetes.io/name": name},
			},
			Template: s.workerPodTemplate(sessionID, sessionapi.KindController, m, labels, wakeReason, tokenSecretName),
		},
	}
}

func (s kubernetesSubstrate) taskJob(
	sessionID string,
	m *sessionapi.Manifest,
	wakeReason string,
	tokenSecretName string,
) *batchv1.Job {
	name := workerWorkloadName(sessionID, sessionapi.KindTask)
	namespace := workerNamespace(sessionID, sessionapi.KindTask)
	labels := workerLabels(name, sessionID, sessionapi.KindTask, ptrValue(m.ParentSessionID))
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   namespace,
			Labels:      labels,
			Annotations: s.workerAnnotations(m, wakeReason),
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: int32Ptr(0),
			Template:     s.workerPodTemplate(sessionID, sessionapi.KindTask, m, labels, wakeReason, tokenSecretName),
		},
	}
}

func (s kubernetesSubstrate) workerPodTemplate(
	sessionID string,
	kind sessionapi.SessionKind,
	m *sessionapi.Manifest,
	labels map[string]string,
	wakeReason string,
	tokenSecretName string,
) corev1.PodTemplateSpec {
	sessionDir := s.stateMountRoot + "/sessions/" + sessionID
	annotations := s.workerAnnotations(m, wakeReason)
	volumes := []corev1.Volume{
		{Name: "telos-runtime", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
	}
	if s.stateStorageClass != "" {
		volumes = append([]corev1.Volume{{
			Name: "telos-state",
			VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
				ClaimName: statePVCName,
				ReadOnly:  false,
			}},
		}}, volumes...)
		annotations["telos.ai/state-volume"] = "pvc"
		annotations["telos.ai/state-storage-class"] = s.stateStorageClass
	} else {
		volumes = append([]corev1.Volume{{
			Name: "telos-state",
			VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{
				Path: s.stateNodeRoot,
				Type: hostPathTypePtr(corev1.HostPathDirectoryOrCreate),
			}},
		}}, volumes...)
		annotations["telos.ai/state-volume"] = "hostPath"
		annotations["telos.ai/state-volume-warning"] = "hostPath state mount enabled; configure kubernetes.state_storage_class to use PVC-backed state"
		if !s.allowHostPathState {
			annotations["telos.ai/state-volume-warning"] = "hostPath state mount used without explicit allow_host_path_state opt-in"
		}
	}
	mounts := []corev1.VolumeMount{
		{Name: "telos-state", MountPath: s.stateMountRoot},
		{Name: "telos-state", MountPath: s.stateHostRoot},
		{Name: "telos-runtime", MountPath: s.runtimeMountPath},
	}
	podSpec := corev1.PodSpec{
		SecurityContext:               agentPodSecurityContext(),
		ServiceAccountName:            "agent",
		TerminationGracePeriodSeconds: int64Ptr(30),
		InitContainers: []corev1.Container{{
			Name:            "install-telos-runtime",
			Image:           s.agentImage,
			ImagePullPolicy: pullPolicy(s.agentImage),
			SecurityContext: agentContainerSecurityContext(),
			Command:         []string{"bash", "-lc", s.runtimeInstallScript()},
			VolumeMounts: []corev1.VolumeMount{
				{Name: "telos-runtime", MountPath: s.runtimeMountPath},
			},
		}},
		Containers: []corev1.Container{{
			Name:            "worker",
			Image:           s.agentImage,
			ImagePullPolicy: pullPolicy(s.agentImage),
			SecurityContext: agentContainerSecurityContext(),
			Command:         []string{s.runtimeTelosdPath, "--session-dir", sessionDir},
			Env:             s.workerEnv(sessionID, m, tokenSecretName),
			EnvFrom: []corev1.EnvFromSource{{
				SecretRef: &corev1.SecretEnvSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: s.agentSecretName},
				},
			}},
			VolumeMounts: mounts,
		}},
		Volumes: volumes,
	}
	if kind == sessionapi.KindTask {
		podSpec.RestartPolicy = corev1.RestartPolicyNever
	}
	if s.imagePullSecret != "" {
		podSpec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: s.imagePullSecret}}
	}
	return corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: podSpec,
	}
}

func (s kubernetesSubstrate) workerEnv(sessionID string, m *sessionapi.Manifest, tokenSecretName string) []corev1.EnvVar {
	env := []corev1.EnvVar{
		{Name: "PATH", Value: s.runtimeMountPath + ":/usr/local/bin:/bin:/usr/bin:/sbin:/usr/sbin"},
		{
			Name: s.agentSecretKey,
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: s.agentSecretName},
				Key:                  s.agentSecretKey,
				Optional:             boolPtr(true),
			}},
		},
		{Name: "TELOS_SESSION_ID", Value: sessionID},
		{Name: "TELOS_PARENT_SESSION_ID", Value: ptrValue(m.ParentSessionID)},
		{Name: "TELOS_SESSION_DIR", Value: s.stateMountRoot + "/sessions"},
		{Name: "TELOS_STATE_HOST_ROOT", Value: s.stateHostRoot},
		fieldEnv("TELOS_RUNNER_POD_NAME", "metadata.name"),
		fieldEnv("TELOS_RUNNER_POD_NAMESPACE", "metadata.namespace"),
		fieldEnv("TELOS_SPEC_VERSION", "metadata.annotations['telos.ai/spec-version']"),
		fieldEnv("TELOS_SPEC_SHA256", "metadata.annotations['telos.ai/spec-sha256']"),
		fieldEnv("TELOS_WAKE_REASON", "metadata.annotations['telos.ai/wake-reason']"),
		fieldEnv("TELOS_WAKE_ID", "metadata.annotations['telos.ai/wake-id']"),
	}
	if tokenSecretName != "" {
		env = append(env, corev1.EnvVar{
			Name: "TELOS_API_TOKEN",
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: tokenSecretName},
				Key:                  sessionAPITokenSecretKey,
			}},
		})
	}
	if s.billingEnvID != "" {
		env = append(env, corev1.EnvVar{Name: "TELOS_ENV_ID", Value: s.billingEnvID})
	}
	if s.billingEndpoint != "" {
		env = append(env, corev1.EnvVar{Name: "TELOS_BILLING_ENDPOINT", Value: s.billingEndpoint})
	}
	if s.managedGatewayEnabled() && s.billingEnvID != "" && s.billing != nil && s.billing.configured() {
		env = append(env, corev1.EnvVar{Name: "TELOS_COST_HARD_LIMIT", Value: "true"})
	}
	return env
}

func (s kubernetesSubstrate) recreateSessionAPITokenSecret(ctx context.Context, namespace string, session *sessionapi.Session, kind sessionapi.SessionKind, m *sessionapi.Manifest) (string, error) {
	name := sessionAPITokenSecretName(session.SessionID)
	previousToken := s.existingSessionAPITokenSecret(ctx, namespace, name)
	token, err := mintSessionAPIToken(session, kind, m, previousToken)
	if err != nil {
		return "", err
	}
	if err := s.deleteSecret(ctx, namespace, name); err != nil {
		return "", fmt.Errorf("delete session API token secret %s/%s: %w", namespace, name, err)
	}
	if err := s.createSessionAPITokenSecret(ctx, namespace, name, token); err != nil {
		return "", fmt.Errorf("create session API token secret %s/%s: %w", namespace, name, err)
	}
	return name, nil
}

func (s kubernetesSubstrate) existingSessionAPITokenSecret(ctx context.Context, namespace string, name string) string {
	secret, err := s.client.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(secret.Data[sessionAPITokenSecretKey]))
}

func mintSessionAPIToken(session *sessionapi.Session, kind sessionapi.SessionKind, m *sessionapi.Manifest, previousTokens ...string) (string, error) {
	if session == nil {
		return "", fmt.Errorf("mint session API token: session is required")
	}
	access, err := sessionapi.NewScopedToken(session.SessionID, kind)
	if err == nil {
		store, storeErr := fileStoreForSession(session)
		if storeErr == nil {
			if indexErr := store.ReplaceWorkerScopedToken(session.SessionID, kind, access, previousTokens...); indexErr == nil {
				return access.APIToken, nil
			} else {
				err = indexErr
			}
		} else {
			err = storeErr
		}
	}
	if m != nil && m.Access != nil && strings.TrimSpace(m.Access.APIToken) != "" {
		return strings.TrimSpace(m.Access.APIToken), nil
	}
	if err != nil {
		return "", fmt.Errorf("mint session API token: %w", err)
	}
	return "", fmt.Errorf("mint session API token: no token issued")
}

func fileStoreForSession(session *sessionapi.Session) (*sessionapi.FileStore, error) {
	if session == nil {
		return nil, fmt.Errorf("session is required")
	}
	sessionDir := strings.TrimSpace(ptrValue(session.SessionDir))
	if sessionDir == "" {
		return nil, fmt.Errorf("session_dir is required")
	}
	root := filepath.Dir(sessionDir)
	if root == "." || root == "" {
		return nil, fmt.Errorf("invalid session_dir %q", sessionDir)
	}
	return sessionapi.NewFileStore(root, sessionapi.RuntimeCloud), nil
}

func sessionAPITokenSecretName(sessionID string) string {
	return "telos-session-api-" + sessionShortID(sessionID)
}

func (s kubernetesSubstrate) createSessionAPITokenSecret(ctx context.Context, namespace string, name string, token string) error {
	secret, err := s.sessionAPITokenSecret(ctx, namespace, name, token)
	if err != nil {
		return err
	}
	_, err = s.client.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return s.createOrUpdateSecret(ctx, secret)
	}
	return err
}

func (s kubernetesSubstrate) sessionAPITokenSecret(ctx context.Context, namespace string, name string, token string) (*corev1.Secret, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, fmt.Errorf("session API token is required")
	}
	ownerRef, err := s.workerNamespaceOwnerReference(ctx, namespace)
	if err != nil {
		return nil, err
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       namespace,
			Labels:          map[string]string{"telos/role": "session-api-token"},
			OwnerReferences: []metav1.OwnerReference{ownerRef},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{sessionAPITokenSecretKey: []byte(token)},
	}, nil
}

func (s kubernetesSubstrate) workerNamespaceOwnerReference(ctx context.Context, namespace string) (metav1.OwnerReference, error) {
	ns, err := s.client.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if err != nil {
		return metav1.OwnerReference{}, fmt.Errorf("read worker namespace %s: %w", namespace, err)
	}
	uid := ns.UID
	if uid == "" {
		uid = types.UID(namespace)
	}
	return metav1.OwnerReference{
		APIVersion: "v1",
		Kind:       "Namespace",
		Name:       namespace,
		UID:        uid,
	}, nil
}

func fieldEnv(name string, fieldPath string) corev1.EnvVar {
	return corev1.EnvVar{
		Name: name,
		ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{
			FieldPath: fieldPath,
		}},
	}
}

func agentContainerSecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		RunAsNonRoot:             boolPtr(true),
		RunAsUser:                int64Ptr(1000),
		RunAsGroup:               int64Ptr(1000),
		AllowPrivilegeEscalation: boolPtr(false),
	}
}

func agentPodSecurityContext() *corev1.PodSecurityContext {
	return &corev1.PodSecurityContext{
		FSGroup: int64Ptr(1000),
	}
}

func workerLabels(name string, sessionID string, kind sessionapi.SessionKind, parent string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name": name,
		"telos/role":             "worker",
		"telos/kind":             string(kind),
		"telos/session":          sessionID,
		"telos/parent":           parent,
	}
}

func (s kubernetesSubstrate) workerAnnotations(m *sessionapi.Manifest, wakeReason string) map[string]string {
	annotations := map[string]string{"telos.ai/runtime-version": s.runtimeVersion}
	if m.CurrentSpecVersion != nil {
		annotations["telos.ai/spec-version"] = fmt.Sprintf("%d", *m.CurrentSpecVersion)
		if sha := specSHAForVersion(m, *m.CurrentSpecVersion); sha != "" {
			annotations["telos.ai/spec-sha256"] = sha
		}
	}
	if wakeReason != "" {
		annotations["telos.ai/wake-reason"] = wakeReason
		annotations["telos.ai/wake-id"] = wakeReason + ":" + time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	}
	return annotations
}

func (s kubernetesSubstrate) createNamespaceIfMissing(ctx context.Context, name string) error {
	_, err := s.client.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	_, err = s.client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: workerNamespaceLabels},
	}, metav1.CreateOptions{})
	return err
}

func (s kubernetesSubstrate) deleteNamespace(ctx context.Context, name string) error {
	err := s.client.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func (s kubernetesSubstrate) createOrUpdateServiceAccount(ctx context.Context, desired *corev1.ServiceAccount) error {
	current, err := s.client.CoreV1().ServiceAccounts(desired.Namespace).Get(ctx, desired.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = s.client.CoreV1().ServiceAccounts(desired.Namespace).Create(ctx, desired, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	desired.ResourceVersion = current.ResourceVersion
	_, err = s.client.CoreV1().ServiceAccounts(desired.Namespace).Update(ctx, desired, metav1.UpdateOptions{})
	return err
}

func (s kubernetesSubstrate) createOrUpdateSecret(ctx context.Context, desired *corev1.Secret) error {
	current, err := s.client.CoreV1().Secrets(desired.Namespace).Get(ctx, desired.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = s.client.CoreV1().Secrets(desired.Namespace).Create(ctx, desired, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	desired.ResourceVersion = current.ResourceVersion
	_, err = s.client.CoreV1().Secrets(desired.Namespace).Update(ctx, desired, metav1.UpdateOptions{})
	return err
}

const statePVCName = "telos-state"

func (s kubernetesSubstrate) statePersistentVolumeClaim(namespace string) *corev1.PersistentVolumeClaim {
	storageClass := s.stateStorageClass
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      statePVCName,
			Namespace: namespace,
			Labels:    map[string]string{"telos/role": "worker-state"},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			StorageClassName: &storageClass,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse(s.stateStorageSize),
				},
			},
		},
	}
}

func (s kubernetesSubstrate) createOrUpdatePersistentVolumeClaim(ctx context.Context, desired *corev1.PersistentVolumeClaim) error {
	current, err := s.client.CoreV1().PersistentVolumeClaims(desired.Namespace).Get(ctx, desired.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = s.client.CoreV1().PersistentVolumeClaims(desired.Namespace).Create(ctx, desired, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	desired.ResourceVersion = current.ResourceVersion
	_, err = s.client.CoreV1().PersistentVolumeClaims(desired.Namespace).Update(ctx, desired, metav1.UpdateOptions{})
	return err
}

func (s kubernetesSubstrate) scrubManagedAgentSecrets(ctx context.Context) error {
	if !s.managedGatewayEnabled() {
		return nil
	}
	secrets, err := s.client.CoreV1().Secrets("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, secret := range secrets.Items {
		if !s.shouldScrubManagedAgentSecret(secret) {
			continue
		}
		if len(secret.Data) == 0 {
			continue
		}
		updated := secret.DeepCopy()
		if !scrubManagedDirectProviderEnv(updated.Data, s.agentSecretKey) {
			continue
		}
		if _, err := s.client.CoreV1().Secrets(updated.Namespace).Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
			return err
		}
	}
	return nil
}

func (s kubernetesSubstrate) shouldScrubManagedAgentSecret(secret corev1.Secret) bool {
	if secret.Name != s.agentSecretName {
		return false
	}
	if secret.Namespace == "" || secret.Namespace == s.envNamespace {
		return false
	}
	if secret.Labels["telos/role"] == "worker-agent" {
		return true
	}
	return strings.HasPrefix(secret.Namespace, "ns-ctrl-") || strings.HasPrefix(secret.Namespace, "ns-task-")
}

func (s kubernetesSubstrate) deleteSecret(ctx context.Context, namespace string, name string) error {
	err := s.client.CoreV1().Secrets(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func (s kubernetesSubstrate) createOrUpdateAgentSecret(ctx context.Context, namespace string, credential *controlSessionKey) error {
	secret := s.agentSecret(namespace, credential)
	source, err := s.client.CoreV1().Secrets(s.envNamespace).Get(ctx, s.agentSecretName, metav1.GetOptions{})
	if err == nil {
		secret.Type = source.Type
		secret.Data = cloneByteMap(source.Data)
		if secret.Data == nil {
			secret.Data = map[string][]byte{}
		}
		removeLegacyGatewayEnv(secret.Data)
		if s.managedGatewayEnabled() {
			removeDirectProviderEnv(secret.Data)
		}
		applyConfiguredGatewayAPIKey(secret.Data, s.agentSecretKey)
		applyConfiguredGatewayEnv(secret.Data)
		s.applyBillingCredential(secret.Data)
		applyGatewayCredential(secret.Data, s.agentSecretKey, credential)
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	if !s.managedGatewayEnabled() && !hasGatewayIntent(secret.Data, s.agentSecretKey) {
		applyConfiguredDirectProviderEnv(secret.Data)
	}
	if !hasGatewayIntent(secret.Data, s.agentSecretKey) {
		clearGatewayCredentialEnv(secret.Data)
	}
	if hasGatewayIntent(secret.Data, s.agentSecretKey) {
		if gatewayAPIKeyValue(secret.Data, s.agentSecretKey) == "" {
			return fmt.Errorf("%s is required to launch a worker", gatewayAPIKeyEnv)
		}
		if !secretHasGatewayBaseURL(secret.Data) {
			return fmt.Errorf("%s is required to launch a worker", gatewayBaseURLEnv)
		}
	}
	return s.createOrUpdateSecret(ctx, secret)
}

func (s kubernetesSubstrate) sessionGatewayCredential(ctx context.Context, sessionID, parentSessionID, userAuthorization string, userOrgID string, modelProfile sessionapi.ModelProfile) (*controlSessionKey, error) {
	if !s.managedGatewayEnabled() || s.billing == nil || !s.billing.configured() {
		return nil, nil
	}
	modelProfile, err := sessionapi.NormalizeModelProfile(string(modelProfile))
	if err != nil {
		return nil, err
	}
	lock := sessionGatewayLock(sessionID)
	lock.Lock()
	defer lock.Unlock()
	secretName := sessionGatewaySecretName(sessionID)
	current, err := s.client.CoreV1().Secrets(s.envNamespace).Get(ctx, secretName, metav1.GetOptions{})
	if err == nil {
		credential := credentialFromSecret(current, s.agentSecretKey)
		if credential != nil {
			if credential.ModelProfile == "" {
				credential.ModelProfile = sessionapi.ModelProfileStandard
			}
			if credential.ModelProfile != modelProfile {
				return nil, fmt.Errorf("cached session gateway key profile mismatch for %s: cached %s, requested %s", sessionID, credential.ModelProfile, modelProfile)
			}
			return credential, nil
		}
	} else if !apierrors.IsNotFound(err) {
		return nil, err
	}
	minted, err := s.billing.MintSessionKey(sessionID, parentSessionID, userAuthorization, userOrgID, modelProfile)
	if err != nil {
		return nil, err
	}
	secret := sessionGatewaySecret(s.envNamespace, secretName, s.agentSecretKey, minted)
	if err := s.createOrUpdateSecret(ctx, secret); err != nil {
		return nil, err
	}
	return &minted, nil
}

func sessionGatewayLock(sessionID string) *sync.Mutex {
	value, _ := sessionGatewayLocks.LoadOrStore(sessionID, &sync.Mutex{})
	return value.(*sync.Mutex)
}

func (s kubernetesSubstrate) applyBillingCredential(data map[string][]byte) {
	if data == nil {
		return
	}
	if !s.managedGatewayEnabled() {
		return
	}
	if s.billingEnvID != "" {
		data["TELOS_ENV_ID"] = []byte(s.billingEnvID)
	}
	if s.billingEndpoint != "" {
		data["TELOS_BILLING_ENDPOINT"] = []byte(s.billingEndpoint)
	}
}

func (s kubernetesSubstrate) managedGatewayEnabled() bool {
	return managedGatewayModeEnabled()
}

func sessionGatewaySecretName(sessionID string) string {
	return "telos-session-gateway-" + sessionShortID(sessionID)
}

func sessionGatewaySecret(namespace string, name string, keyName string, credential controlSessionKey) *corev1.Secret {
	data := map[string][]byte{
		keyName:           []byte(credential.APIKey),
		gatewayAPIKeyEnv:  []byte(credential.APIKey),
		gatewayBaseURLEnv: []byte(credential.BaseURL),
		gatewayModeEnv:    []byte("managed"),
	}
	if credential.Transport != "" {
		data[gatewayTransportEnv] = []byte(credential.Transport)
	}
	if credential.Kind != "" {
		data[gatewayKindEnv] = []byte(credential.Kind)
	}
	if headers := gatewayHeadersJSON(credential.Headers); headers != "" {
		data[gatewayHeadersEnv] = []byte(headers)
	}
	if credential.KeyAlias != "" {
		data[gatewayKeyAliasEnv] = []byte(credential.KeyAlias)
	}
	if credential.ModelProfile != "" {
		data[modelProfileEnv] = []byte(credential.ModelProfile)
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"telos/role": "session-gateway",
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: data,
	}
}

func credentialFromSecret(secret *corev1.Secret, keyName string) *controlSessionKey {
	if secret == nil {
		return nil
	}
	apiKey := gatewayAPIKeyValue(secret.Data, keyName)
	baseURL := strings.TrimSpace(string(secret.Data[gatewayBaseURLEnv]))
	if apiKey == "" || baseURL == "" {
		return nil
	}
	profile, err := sessionapi.NormalizeModelProfile(strings.TrimSpace(string(secret.Data[modelProfileEnv])))
	if err != nil {
		return nil
	}
	return &controlSessionKey{
		Credential: gatewaycred.Credential{
			BaseURL:      strings.TrimRight(baseURL, "/"),
			APIKey:       apiKey,
			Transport:    gatewaycred.Transport(strings.TrimSpace(string(secret.Data[gatewayTransportEnv]))),
			Kind:         gatewaycred.Kind(strings.TrimSpace(string(secret.Data[gatewayKindEnv]))),
			Headers:      gatewayHeadersFromSecret(secret.Data),
			ModelProfile: profile,
		},
		KeyAlias: strings.TrimSpace(string(secret.Data[gatewayKeyAliasEnv])),
	}
}

func applyGatewayCredential(data map[string][]byte, keyName string, credential *controlSessionKey) {
	if credential == nil {
		return
	}
	clearOptionalGatewayCredentialEnv(data)
	data[keyName] = []byte(credential.APIKey)
	data[gatewayAPIKeyEnv] = []byte(credential.APIKey)
	data[gatewayBaseURLEnv] = []byte(credential.BaseURL)
	data[gatewayModeEnv] = []byte("managed")
	if credential.Transport != "" {
		data[gatewayTransportEnv] = []byte(credential.Transport)
	}
	if credential.Kind != "" {
		data[gatewayKindEnv] = []byte(credential.Kind)
	}
	if headers := gatewayHeadersJSON(credential.Headers); headers != "" {
		data[gatewayHeadersEnv] = []byte(headers)
	}
	if credential.KeyAlias != "" {
		data[gatewayKeyAliasEnv] = []byte(credential.KeyAlias)
	}
	if credential.ModelProfile != "" {
		data[modelProfileEnv] = []byte(credential.ModelProfile)
	}
}

func removeLegacyGatewayEnv(data map[string][]byte) {
	if data == nil {
		return
	}
	for _, name := range legacyGatewayEnvNames {
		delete(data, name)
	}
	delete(data, gatewayKeyAliasEnv)
}

func clearOptionalGatewayCredentialEnv(data map[string][]byte) {
	if data == nil {
		return
	}
	for _, name := range optionalGatewayCredentialEnvNames {
		delete(data, name)
	}
}

func clearGatewayCredentialEnv(data map[string][]byte) {
	if data == nil {
		return
	}
	delete(data, gatewayAPIKeyEnv)
	delete(data, gatewayBaseURLEnv)
	clearOptionalGatewayCredentialEnv(data)
}

func removeDirectProviderEnv(data map[string][]byte) {
	if data == nil {
		return
	}
	for _, name := range directProviderKeyNames {
		delete(data, name)
	}
}

// scrubManagedDirectProviderEnv strips direct provider keys from an existing
// worker agent secret, sparing the agent secret key slot when it holds the
// managed gateway key itself. Returns whether anything changed.
func scrubManagedDirectProviderEnv(data map[string][]byte, keyName string) bool {
	if data == nil {
		return false
	}
	gatewayKey := strings.TrimSpace(string(data[gatewayAPIKeyEnv]))
	changed := false
	for _, name := range directProviderKeyNames {
		value, ok := data[name]
		if !ok {
			continue
		}
		if name == keyName && gatewayKey != "" && strings.TrimSpace(string(value)) == gatewayKey {
			continue
		}
		delete(data, name)
		changed = true
	}
	return changed
}

func applyConfiguredDirectProviderEnv(data map[string][]byte) {
	if data == nil {
		return
	}
	for _, name := range directProviderKeyNames {
		if strings.TrimSpace(string(data[name])) != "" {
			continue
		}
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			data[name] = []byte(value)
		}
	}
}

func applyConfiguredGatewayAPIKey(data map[string][]byte, keyName string) {
	if data == nil {
		return
	}
	value := strings.TrimSpace(os.Getenv(keyName))
	if value == "" && keyName != gatewayAPIKeyEnv {
		value = strings.TrimSpace(os.Getenv(gatewayAPIKeyEnv))
	}
	if value == "" {
		return
	}
	data[keyName] = []byte(value)
	data[gatewayAPIKeyEnv] = []byte(value)
}

func applyConfiguredGatewayEnv(data map[string][]byte) {
	if data == nil {
		return
	}
	for _, name := range gatewayEnvNames {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			data[name] = []byte(value)
		}
	}
}

func hasGatewayIntent(data map[string][]byte, keyName string) bool {
	return strings.TrimSpace(string(data[gatewayAPIKeyEnv])) != "" ||
		strings.TrimSpace(string(data[keyName])) != "" ||
		strings.TrimSpace(string(data[gatewayBaseURLEnv])) != ""
}

func gatewayAPIKeyValue(data map[string][]byte, keyName string) string {
	if value := strings.TrimSpace(string(data[gatewayAPIKeyEnv])); value != "" {
		return value
	}
	return strings.TrimSpace(string(data[keyName]))
}

func secretHasGatewayBaseURL(data map[string][]byte) bool {
	return strings.TrimSpace(string(data[gatewayBaseURLEnv])) != ""
}

func gatewayHeadersJSON(headers map[string]string) string {
	if len(headers) == 0 {
		return ""
	}
	data, err := json.Marshal(headers)
	if err != nil {
		return ""
	}
	return string(data)
}

func gatewayHeadersFromSecret(data map[string][]byte) map[string]string {
	raw := strings.TrimSpace(string(data[gatewayHeadersEnv]))
	if raw == "" {
		return nil
	}
	var headers map[string]string
	if err := json.Unmarshal([]byte(raw), &headers); err != nil {
		return nil
	}
	return cloneStringMap(headers)
}

func (s kubernetesSubstrate) copySecret(ctx context.Context, sourceNamespace string, targetNamespace string, name string) error {
	source, err := s.client.CoreV1().Secrets(sourceNamespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	desired := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   targetNamespace,
			Labels:      source.Labels,
			Annotations: source.Annotations,
		},
		Type: source.Type,
		Data: cloneByteMap(source.Data),
	}
	return s.createOrUpdateSecret(ctx, desired)
}

func (s kubernetesSubstrate) createOrUpdateClusterRole(ctx context.Context, desired *rbacv1.ClusterRole) error {
	current, err := s.client.RbacV1().ClusterRoles().Get(ctx, desired.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = s.client.RbacV1().ClusterRoles().Create(ctx, desired, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	desired.ResourceVersion = current.ResourceVersion
	_, err = s.client.RbacV1().ClusterRoles().Update(ctx, desired, metav1.UpdateOptions{})
	return err
}

func (s kubernetesSubstrate) deleteClusterRole(ctx context.Context, name string) error {
	err := s.client.RbacV1().ClusterRoles().Delete(ctx, name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func (s kubernetesSubstrate) createOrUpdateClusterRoleBinding(ctx context.Context, desired *rbacv1.ClusterRoleBinding) error {
	current, err := s.client.RbacV1().ClusterRoleBindings().Get(ctx, desired.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = s.client.RbacV1().ClusterRoleBindings().Create(ctx, desired, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	desired.ResourceVersion = current.ResourceVersion
	_, err = s.client.RbacV1().ClusterRoleBindings().Update(ctx, desired, metav1.UpdateOptions{})
	return err
}

func (s kubernetesSubstrate) deleteClusterRoleBinding(ctx context.Context, name string) error {
	err := s.client.RbacV1().ClusterRoleBindings().Delete(ctx, name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func (s kubernetesSubstrate) createOrUpdateNetworkPolicy(ctx context.Context, desired *networkingv1.NetworkPolicy) error {
	current, err := s.client.NetworkingV1().NetworkPolicies(desired.Namespace).Get(ctx, desired.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = s.client.NetworkingV1().NetworkPolicies(desired.Namespace).Create(ctx, desired, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	desired.ResourceVersion = current.ResourceVersion
	_, err = s.client.NetworkingV1().NetworkPolicies(desired.Namespace).Update(ctx, desired, metav1.UpdateOptions{})
	return err
}

func (s kubernetesSubstrate) createOrUpdateDeployment(ctx context.Context, desired *appsv1.Deployment) error {
	current, err := s.client.AppsV1().Deployments(desired.Namespace).Get(ctx, desired.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = s.client.AppsV1().Deployments(desired.Namespace).Create(ctx, desired, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	desired.ResourceVersion = current.ResourceVersion
	_, err = s.client.AppsV1().Deployments(desired.Namespace).Update(ctx, desired, metav1.UpdateOptions{})
	return err
}

func (s kubernetesSubstrate) createJobIfMissing(ctx context.Context, desired *batchv1.Job) error {
	_, err := s.client.BatchV1().Jobs(desired.Namespace).Get(ctx, desired.Name, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	_, err = s.client.BatchV1().Jobs(desired.Namespace).Create(ctx, desired, metav1.CreateOptions{})
	return err
}

func serviceAccount(namespace string) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: namespace},
	}
}

func workerClusterRole(namespace string) *rbacv1.ClusterRole {
	name := "agent-worker-" + namespace
	return &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Rules: []rbacv1.PolicyRule{
			// Worker apply path: agent-created Kubernetes app specs need CRUD for
			// namespaced core resources inside their session namespace.
			{
				APIGroups: []string{""},
				Resources: []string{
					"pods", "services", "configmaps", "secrets", "events", "persistentvolumeclaims",
				},
				Verbs: namespacedWriteVerbs(),
			},
			// Worker diagnostics path: tools may stream logs for pods created by
			// the session when debugging deployments.
			{APIGroups: []string{""}, Resources: []string{"pods/log"}, Verbs: []string{"get", "list"}},
			// Worker interactive diagnostics path: exec is create-only in the
			// Kubernetes API and is used for explicit pod debugging commands.
			{APIGroups: []string{""}, Resources: []string{"pods/exec"}, Verbs: []string{"create"}},
			// Worker apply path: app manifests most commonly manage Deployments.
			{APIGroups: []string{"apps"}, Resources: []string{"deployments"}, Verbs: namespacedWriteVerbs()},
			// Worker task path: batch Jobs are created for session-managed tasks.
			{APIGroups: []string{"batch"}, Resources: []string{"jobs"}, Verbs: namespacedWriteVerbs()},
			// Worker network path: product routes may require in-namespace
			// NetworkPolicy and Ingress objects.
			{APIGroups: []string{"networking.k8s.io"}, Resources: []string{"networkpolicies", "ingresses"}, Verbs: namespacedWriteVerbs()},
		},
	}
}

func workerClusterRoleBinding(namespace string) *rbacv1.ClusterRoleBinding {
	name := "agent-worker-" + namespace
	return &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name + "-binding"},
		Subjects: []rbacv1.Subject{{
			Kind:      "ServiceAccount",
			Name:      "agent",
			Namespace: namespace,
		}},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     name,
		},
	}
}

func workerNetworkPolicies(namespace string, egressCIDRs []string) []*networkingv1.NetworkPolicy {
	egress := []networkingv1.NetworkPolicyEgressRule{
		{Ports: []networkingv1.NetworkPolicyPort{networkPort(53, corev1.ProtocolUDP), networkPort(53, corev1.ProtocolTCP)}},
	}
	for _, cidr := range egressCIDRs {
		cidr = strings.TrimSpace(cidr)
		if cidr == "" {
			continue
		}
		egress = append(egress, networkingv1.NetworkPolicyEgressRule{
			To:    []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: cidr}}},
			Ports: []networkingv1.NetworkPolicyPort{networkPort(443, corev1.ProtocolTCP), networkPort(80, corev1.ProtocolTCP)},
		})
	}
	return []*networkingv1.NetworkPolicy{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "agent-egress", Namespace: namespace},
			Spec: networkingv1.NetworkPolicySpec{
				PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"telos/role": "worker"}},
				PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
				Egress:      egress,
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "agent-ingress-deny", Namespace: namespace},
			Spec: networkingv1.NetworkPolicySpec{
				PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"telos/role": "worker"}},
				PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
				Ingress:     []networkingv1.NetworkPolicyIngressRule{},
			},
		},
	}
}

func networkPort(port int, protocol corev1.Protocol) networkingv1.NetworkPolicyPort {
	return networkingv1.NetworkPolicyPort{
		Protocol: &protocol,
		Port:     intStrPtr(intstr.FromInt32(int32(port))),
	}
}

func namespacedWriteVerbs() []string {
	return []string{"create", "get", "list", "update", "patch", "delete"}
}

func specSHAForVersion(m *sessionapi.Manifest, version int) string {
	for _, item := range m.SpecVersions {
		if itemVersion, ok := item["version"].(float64); ok && int(itemVersion) == version {
			if sha, ok := item["spec_sha256"].(string); ok {
				return sha
			}
		}
		if itemVersion, ok := item["version"].(int); ok && itemVersion == version {
			if sha, ok := item["spec_sha256"].(string); ok {
				return sha
			}
		}
	}
	return ""
}

func sessionWorkerKind(session *sessionapi.Session) (sessionapi.SessionKind, error) {
	if session == nil || session.SessionKind == nil {
		return "", errors.New("session_kind is required to launch a worker")
	}
	switch *session.SessionKind {
	case sessionapi.KindController, sessionapi.KindTask:
		return *session.SessionKind, nil
	default:
		return "", fmt.Errorf("invalid session_kind %q", *session.SessionKind)
	}
}

func workerWorkloadName(sessionID string, kind sessionapi.SessionKind) string {
	short := sessionShortID(sessionID)
	if kind == sessionapi.KindController {
		return "controller-" + short
	}
	return "task-" + short
}

func workerNamespace(sessionID string, kind sessionapi.SessionKind) string {
	short := sessionShortID(sessionID)
	if kind == sessionapi.KindController {
		return "ns-ctrl-" + short
	}
	return "ns-task-" + short
}

func sessionShortID(sessionID string) string {
	parts := strings.Split(sessionID, "_")
	return parts[len(parts)-1]
}

func (s kubernetesSubstrate) runtimeInstallScript() string {
	amd64Telos := s.runtimeArtifactDigest("telos", "linux", "amd64")
	amd64Telosd := s.runtimeArtifactDigest("telosd", "linux", "amd64")
	arm64Telos := s.runtimeArtifactDigest("telos", "linux", "arm64")
	arm64Telosd := s.runtimeArtifactDigest("telosd", "linux", "arm64")
	return fmt.Sprintf(`set -euo pipefail
base_url=%q
version=%q
os=linux
arch="$(uname -m)"
case "$arch" in
  x86_64|amd64) arch=amd64; telos_sha256=%q; telosd_sha256=%q ;;
  aarch64|arm64) arch=arm64; telos_sha256=%q; telosd_sha256=%q ;;
  *) echo "unsupported architecture: $arch" >&2; exit 1 ;;
esac

if [ -z "$version" ] || [ "$version" = "latest" ]; then
  echo "runtime artifact_version must be pinned; refusing to fetch latest" >&2
  exit 1
fi
if [ -z "$telos_sha256" ] || [ -z "$telosd_sha256" ]; then
  echo "runtime sha256 digests must be configured for $os/$arch" >&2
  exit 1
fi
resolved_base_url="$base_url/$version"

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

mkdir -p %s

download_verified() {
  artifact="$1"
  dest="$2"
  expected="$3"
  curl -fsSL "$resolved_base_url/$artifact" -o "$dest"
  actual="$(sha256sum "$dest" | awk '{ print $1 }')"
  if [ "$actual" != "$expected" ]; then
    echo "checksum verification failed for $artifact" >&2
    exit 1
  fi
}

download_verified "telos-$os-$arch" "$tmp_dir/telos" "$telos_sha256"
download_verified "telosd-$os-$arch" "$tmp_dir/telosd" "$telosd_sha256"
install -m 0755 "$tmp_dir/telos" %s
install -m 0755 "$tmp_dir/telosd" %s

%s --version
%s --version
`, s.runtimeBaseURL, s.runtimeVersion, amd64Telos, amd64Telosd, arm64Telos, arm64Telosd, s.runtimeMountPath, s.runtimeTelosPath, s.runtimeTelosdPath, s.runtimeTelosPath, s.runtimeTelosdPath)
}

func runtimeArtifactDigestMap(artifacts []RuntimeArtifactConfig) map[string]string {
	out := map[string]string{}
	for _, artifact := range artifacts {
		key := runtimeArtifactKey(artifact.Name, artifact.OS, artifact.Arch)
		if key == "" {
			continue
		}
		out[key] = strings.ToLower(strings.TrimSpace(artifact.SHA256))
	}
	return out
}

func (s kubernetesSubstrate) runtimeArtifactDigest(name, osName, arch string) string {
	if s.runtimeArtifacts == nil {
		return ""
	}
	return s.runtimeArtifacts[runtimeArtifactKey(name, osName, arch)]
}

func runtimeArtifactKey(name, osName, arch string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	osName = strings.ToLower(strings.TrimSpace(osName))
	arch = strings.ToLower(strings.TrimSpace(arch))
	if name == "" || osName == "" || arch == "" {
		return ""
	}
	return name + "/" + osName + "/" + arch
}

func verifyArtifactSHA256(path string, expected string) error {
	expected = strings.ToLower(strings.TrimSpace(expected))
	if len(expected) != sha256.Size*2 {
		return fmt.Errorf("invalid sha256 digest %q", expected)
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if actual != expected {
		return fmt.Errorf("sha256 mismatch for %s: got %s want %s", path, actual, expected)
	}
	return nil
}

func pullPolicy(image string) corev1.PullPolicy {
	if !strings.Contains(image, ":") && !strings.Contains(image, "@") {
		return corev1.PullAlways
	}
	if strings.HasSuffix(image, ":latest") {
		return corev1.PullAlways
	}
	return corev1.PullIfNotPresent
}

func int32Ptr(value int32) *int32 {
	return &value
}

func int64Ptr(value int64) *int64 {
	return &value
}

func boolPtr(value bool) *bool {
	return &value
}

func intStrPtr(value intstr.IntOrString) *intstr.IntOrString {
	return &value
}

func hostPathTypePtr(value corev1.HostPathType) *corev1.HostPathType {
	return &value
}

func ptrValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func envOr(name string, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func cloneByteMap(in map[string][]byte) map[string][]byte {
	out := make(map[string][]byte, len(in))
	for key, value := range in {
		out[key] = append([]byte{}, value...)
	}
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		out[key] = strings.TrimSpace(value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
