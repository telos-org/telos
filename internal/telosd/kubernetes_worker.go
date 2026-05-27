package telosd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/telos-org/telos/internal/sessionapi"
)

type kubernetesSubstrate struct {
	client kubernetes.Interface

	agentImage        string
	envNamespace      string
	stateMountRoot    string
	stateHostRoot     string
	stateNodeRoot     string
	imagePullSecret   string
	agentSecretName   string
	agentSecretKey    string
	copySecrets       []string
	runtimeBaseURL    string
	runtimeVersion    string
	runtimeMountPath  string
	runtimeTelosPath  string
	runtimeTelosdPath string
}

const piConfigSecretName = "pi-agent-config"

func newKubernetesSubstrate(cfg Config) (kubernetesSubstrate, error) {
	if strings.TrimSpace(os.Getenv(cfg.Kubernetes.AgentSecretKey)) == "" {
		return kubernetesSubstrate{}, fmt.Errorf("%s is required to launch workers", cfg.Kubernetes.AgentSecretKey)
	}
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
		client:            client,
		agentImage:        cfg.Kubernetes.AgentImage,
		envNamespace:      cfg.Kubernetes.EnvNamespace,
		stateMountRoot:    cfg.Kubernetes.StateMountRoot,
		stateHostRoot:     cfg.Kubernetes.StateHostRoot,
		stateNodeRoot:     cfg.Kubernetes.StateNodeRoot,
		imagePullSecret:   cfg.Kubernetes.ImagePullSecret,
		agentSecretName:   cfg.Kubernetes.AgentSecretName,
		agentSecretKey:    cfg.Kubernetes.AgentSecretKey,
		copySecrets:       append([]string{}, cfg.Kubernetes.CopySecrets...),
		runtimeBaseURL:    cfg.Runtime.ArtifactBaseURL,
		runtimeVersion:    cfg.Runtime.ArtifactVersion,
		runtimeMountPath:  runtimeMountPath,
		runtimeTelosPath:  runtimeMountPath + "/telos",
		runtimeTelosdPath: runtimeMountPath + "/telosd",
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

func (s kubernetesSubstrate) Apply(session *sessionapi.Session, wakeReason string) error {
	kind, err := sessionWorkerKind(session)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	namespace := workerNamespace(session.SessionID, kind)
	if err := s.prepareWorkerNamespace(ctx, namespace); err != nil {
		return err
	}
	m, err := sessionapi.ReadManifest(filepath.Join(ptrValue(session.SessionDir), "session.json"))
	if err != nil {
		return fmt.Errorf("read worker manifest: %w", err)
	}
	switch kind {
	case sessionapi.KindController:
		return s.createOrUpdateDeployment(ctx, s.controllerDeployment(session.SessionID, m, wakeReason))
	case sessionapi.KindTask:
		return s.createJobIfMissing(ctx, s.taskJob(session.SessionID, m, wakeReason))
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
	return stopErr
}

func (s kubernetesSubstrate) prepareWorkerNamespace(ctx context.Context, namespace string) error {
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
	for _, policy := range workerNetworkPolicies(namespace) {
		if err := s.createOrUpdateNetworkPolicy(ctx, policy); err != nil {
			return err
		}
	}
	if err := s.createOrUpdateAgentSecret(ctx, namespace); err != nil {
		return err
	}
	if err := s.copyOptionalSecret(ctx, s.envNamespace, namespace, piConfigSecretName); err != nil {
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

func (s kubernetesSubstrate) agentSecret(namespace string) *corev1.Secret {
	value := strings.TrimSpace(os.Getenv(s.agentSecretKey))
	data := map[string][]byte{}
	if value != "" {
		data[s.agentSecretKey] = []byte(value)
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: s.agentSecretName, Namespace: namespace},
		Type:       corev1.SecretTypeOpaque,
		Data:       data,
	}
}

func (s kubernetesSubstrate) controllerDeployment(
	sessionID string,
	m *sessionapi.Manifest,
	wakeReason string,
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
			Template: s.workerPodTemplate(sessionID, sessionapi.KindController, m, labels, wakeReason),
		},
	}
}

func (s kubernetesSubstrate) taskJob(
	sessionID string,
	m *sessionapi.Manifest,
	wakeReason string,
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
			Template:     s.workerPodTemplate(sessionID, sessionapi.KindTask, m, labels, wakeReason),
		},
	}
}

func (s kubernetesSubstrate) workerPodTemplate(
	sessionID string,
	kind sessionapi.SessionKind,
	m *sessionapi.Manifest,
	labels map[string]string,
	wakeReason string,
) corev1.PodTemplateSpec {
	sessionDir := s.stateMountRoot + "/sessions/" + sessionID
	volumes := []corev1.Volume{
		{
			Name: "telos-state",
			VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{
				Path: s.stateNodeRoot,
				Type: hostPathTypePtr(corev1.HostPathDirectoryOrCreate),
			}},
		},
		{Name: "telos-runtime", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{
			Name: "pi-agent-config-source",
			VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
				SecretName:  piConfigSecretName,
				Optional:    boolPtr(true),
				DefaultMode: int32Ptr(0o440),
			}},
		},
		{Name: "pi-agent-config-home", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
	}
	mounts := []corev1.VolumeMount{
		{Name: "telos-state", MountPath: s.stateMountRoot},
		{Name: "telos-state", MountPath: s.stateHostRoot},
		{Name: "telos-runtime", MountPath: s.runtimeMountPath},
		{Name: "pi-agent-config-home", MountPath: "/home/agent/.pi/agent"},
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
				{Name: "pi-agent-config-source", MountPath: "/telos-pi-agent-config", ReadOnly: true},
				{Name: "pi-agent-config-home", MountPath: "/home/agent/.pi/agent"},
			},
		}},
		Containers: []corev1.Container{{
			Name:            "worker",
			Image:           s.agentImage,
			ImagePullPolicy: pullPolicy(s.agentImage),
			SecurityContext: agentContainerSecurityContext(),
			Command:         []string{s.runtimeTelosdPath, "--session-dir", sessionDir},
			Env:             s.workerEnv(sessionID, m),
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
			Annotations: s.workerAnnotations(m, wakeReason),
		},
		Spec: podSpec,
	}
}

func (s kubernetesSubstrate) workerEnv(sessionID string, m *sessionapi.Manifest) []corev1.EnvVar {
	env := []corev1.EnvVar{
		{Name: "PATH", Value: s.runtimeMountPath + ":/usr/local/bin:/bin:/usr/bin:/sbin:/usr/sbin"},
		{
			Name: s.agentSecretKey,
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: s.agentSecretName},
				Key:                  s.agentSecretKey,
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
	if m.Access != nil && strings.TrimSpace(m.Access.APIToken) != "" {
		env = append(env, corev1.EnvVar{Name: "TELOS_API_TOKEN", Value: m.Access.APIToken})
	}
	return env
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
		ObjectMeta: metav1.ObjectMeta{Name: name},
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

func (s kubernetesSubstrate) createOrUpdateAgentSecret(ctx context.Context, namespace string) error {
	secret := s.agentSecret(namespace)
	source, err := s.client.CoreV1().Secrets(s.envNamespace).Get(ctx, s.agentSecretName, metav1.GetOptions{})
	if err == nil {
		secret.Type = source.Type
		secret.Data = cloneByteMap(source.Data)
		if secret.Data == nil {
			secret.Data = map[string][]byte{}
		}
		if envValue := strings.TrimSpace(os.Getenv(s.agentSecretKey)); envValue != "" {
			secret.Data[s.agentSecretKey] = []byte(envValue)
		}
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	if strings.TrimSpace(string(secret.Data[s.agentSecretKey])) == "" {
		return fmt.Errorf("%s is required to launch a worker", s.agentSecretKey)
	}
	return s.createOrUpdateSecret(ctx, secret)
}

func (s kubernetesSubstrate) copyOptionalSecret(ctx context.Context, sourceNamespace string, targetNamespace string, name string) error {
	if strings.TrimSpace(name) == "" {
		return nil
	}
	err := s.copySecret(ctx, sourceNamespace, targetNamespace, name)
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
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
			{
				APIGroups: []string{""},
				Resources: []string{
					"namespaces", "pods",
					"services", "configmaps", "secrets", "events", "endpoints",
					"persistentvolumeclaims",
				},
				Verbs: workerVerbs(),
			},
			{APIGroups: []string{""}, Resources: []string{"pods/exec"}, Verbs: []string{"create", "get"}},
			{APIGroups: []string{""}, Resources: []string{"nodes", "pods/log", "persistentvolumes", "serviceaccounts"}, Verbs: readVerbs()},
			{APIGroups: []string{"apps"}, Resources: []string{"deployments", "replicasets", "statefulsets", "daemonsets"}, Verbs: workerVerbs()},
			{APIGroups: []string{"batch"}, Resources: []string{"jobs", "cronjobs"}, Verbs: workerVerbs()},
			{APIGroups: []string{"networking.k8s.io"}, Resources: []string{"networkpolicies", "ingresses"}, Verbs: workerVerbs()},
			{
				APIGroups: []string{"rbac.authorization.k8s.io"},
				Resources: []string{"roles", "rolebindings", "clusterroles", "clusterrolebindings"},
				Verbs:     readVerbs(),
			},
			{APIGroups: []string{"storage.k8s.io"}, Resources: []string{"storageclasses"}, Verbs: readVerbs()},
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

func workerNetworkPolicies(namespace string) []*networkingv1.NetworkPolicy {
	return []*networkingv1.NetworkPolicy{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "agent-egress", Namespace: namespace},
			Spec: networkingv1.NetworkPolicySpec{
				PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"telos/role": "worker"}},
				PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
				Egress: []networkingv1.NetworkPolicyEgressRule{
					{Ports: []networkingv1.NetworkPolicyPort{networkPort(53, corev1.ProtocolUDP), networkPort(53, corev1.ProtocolTCP)}},
					{Ports: []networkingv1.NetworkPolicyPort{networkPort(443, corev1.ProtocolTCP)}},
					{To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: "10.0.0.0/8"}}}},
				},
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

func workerVerbs() []string {
	return []string{"create", "get", "list", "watch", "update", "patch", "delete"}
}

func readVerbs() []string {
	return []string{"get", "list", "watch"}
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
	return fmt.Sprintf(`set -euo pipefail
base_url=%q
version=%q
os=linux
arch="$(uname -m)"
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo "unsupported architecture: $arch" >&2; exit 1 ;;
esac

if [ "$version" = "latest" ]; then
  manifest="$(curl -fsSL -H 'Cache-Control: no-cache' "$base_url/latest/manifest.json?$(date +%%s)")"
  version="$(printf '%%s' "$manifest" | jq -r '.version')"
  resolved_base_url="$(printf '%%s' "$manifest" | jq -r '.base_url')"
  if [ -z "$version" ] || [ "$version" = "null" ] || [ -z "$resolved_base_url" ] || [ "$resolved_base_url" = "null" ]; then
    echo "failed to parse Telos runtime manifest" >&2
    exit 1
  fi
else
  resolved_base_url="$base_url/$version"
fi

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

curl -fsSL "$resolved_base_url/SHA256SUMS" -o "$tmp_dir/SHA256SUMS"
mkdir -p %s

download_verified() {
  artifact="$1"
  dest="$2"
  curl -fsSL "$resolved_base_url/$artifact" -o "$dest"
  expected="$(awk -v file="$artifact" '$2 == file { print $1 }' "$tmp_dir/SHA256SUMS")"
  if [ -z "$expected" ]; then
    echo "checksum missing for $artifact" >&2
    exit 1
  fi
  actual="$(sha256sum "$dest" | awk '{ print $1 }')"
  if [ "$actual" != "$expected" ]; then
    echo "checksum verification failed for $artifact" >&2
    exit 1
  fi
}

download_verified "telos-$os-$arch" "$tmp_dir/telos"
download_verified "telosd-$os-$arch" "$tmp_dir/telosd"
install -m 0755 "$tmp_dir/telos" %s
install -m 0755 "$tmp_dir/telosd" %s

mkdir -p /home/agent/.pi/agent
for source in /telos-pi-agent-config/*; do
  [ -e "$source" ] || continue
  cp -L "$source" /home/agent/.pi/agent/
done
chmod 0600 /home/agent/.pi/agent/* 2>/dev/null || true

%s --version
%s --version
`, s.runtimeBaseURL, s.runtimeVersion, s.runtimeMountPath, s.runtimeTelosPath, s.runtimeTelosdPath, s.runtimeTelosPath, s.runtimeTelosdPath)
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
