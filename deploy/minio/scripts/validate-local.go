// Run from video-operator, using its existing module dependencies:
// GOPROXY=off GOSUMDB=off go run ../deploy/minio/scripts/validate-local.go ../deploy/minio
// This program runs ONLY kubectl kustomize and reads local files. No REST calls.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkv1 "k8s.io/api/networking/v1"
	kyaml "k8s.io/apimachinery/pkg/util/yaml"
)

func require(ok bool, message string) {
	if !ok {
		panic(message)
	}
}

func strict(raw []byte, target any) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		panic(err)
	}
}

func checkPod(p corev1.PodSpec) {
	require(p.AutomountServiceAccountToken != nil && !*p.AutomountServiceAccountToken, "service account token must not be mounted")
	require(p.SecurityContext != nil && p.SecurityContext.RunAsNonRoot != nil && *p.SecurityContext.RunAsNonRoot, "non-root pod required")
	for _, v := range p.Volumes {
		require(v.Secret == nil, "credentials must be injected through secretKeyRef environment variables")
		if v.PersistentVolumeClaim != nil {
			require(v.PersistentVolumeClaim.ClaimName == "mansiflix-minio-data", "unexpected or legacy PVC")
		}
	}
	for _, c := range append(append([]corev1.Container{}, p.InitContainers...), p.Containers...) {
		expectedImages := map[string]string{
			"minio":      "mansiflix/minio:source-9e49d5e7a648-amd64-r1",
			"bootstrap":  "mansiflix/minio-mc:source-7394ce0dd2a8-amd64-r2",
			"smoke-test": "mansiflix/minio-mc:source-7394ce0dd2a8-amd64-r2",
			"seed":       "mansiflix/minio-mc:source-7394ce0dd2a8-amd64-r2",
			"verify":     "mansiflix/minio-mc:source-7394ce0dd2a8-amd64-r2",
			"download":   "mansiflix/minio-mc:source-7394ce0dd2a8-amd64-r2",
			"upload":     "mansiflix/minio-mc:source-7394ce0dd2a8-amd64-r2",
			"generate":   "mansiflix/video-worker:demo-88bb1d91e247",
			"encode":     "mansiflix/video-worker:demo-88bb1d91e247",
			"network":    "mansiflix/minio-mc:source-7394ce0dd2a8-amd64-r2",
		}
		expectedImage, reviewedRole := expectedImages[c.Name]
		require(reviewedRole && c.Image == expectedImage, "unreviewed container role or image")
		require(c.ImagePullPolicy == corev1.PullNever, "images must require explicit prior import")
		require(c.SecurityContext != nil && c.SecurityContext.ReadOnlyRootFilesystem != nil && *c.SecurityContext.ReadOnlyRootFilesystem, "read-only container required")
		for _, name := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory, corev1.ResourceEphemeralStorage} {
			req, limit := c.Resources.Requests[name], c.Resources.Limits[name]
			require(!req.IsZero() && !limit.IsZero() && req.Cmp(limit) <= 0, "missing or invalid resource request/limit")
		}
		expectedSecrets := map[string][2]string{}
		if c.Name == "minio" || c.Name == "bootstrap" || c.Name == "smoke-test" || c.Name == "seed" || c.Name == "verify" {
			expectedSecrets["MINIO_ROOT_USER"] = [2]string{"mansiflix-minio-admin", "MINIO_ROOT_USER"}
			expectedSecrets["MINIO_ROOT_PASSWORD"] = [2]string{"mansiflix-minio-admin", "MINIO_ROOT_PASSWORD"}
		}
		if c.Name != "minio" && c.Name != "generate" && c.Name != "encode" && c.Name != "network" {
			expectedSecrets["WORKER_ACCESS_KEY"] = [2]string{"mansiflix-minio-worker", "access-key"}
			expectedSecrets["WORKER_SECRET_KEY"] = [2]string{"mansiflix-minio-worker", "secret-key"}
		}
		require(len(c.EnvFrom) == 0, "unreviewed bulk environment injection")
		seenSecrets := map[string]bool{}
		for _, e := range c.Env {
			if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
				_, reviewed := expectedSecrets[e.Name]
				require(reviewed, "unreviewed Secret environment reference")
			}
			if expected, ok := expectedSecrets[e.Name]; ok {
				require(!seenSecrets[e.Name], "duplicate credential environment variable")
				require(e.Value == "" && e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil, "embedded credential or missing secretKeyRef")
				ref := e.ValueFrom.SecretKeyRef
				require(ref.Name == expected[0] && ref.Key == expected[1], "unexpected credential Secret/key")
				seenSecrets[e.Name] = true
			}
		}
		require(len(seenSecrets) == len(expectedSecrets), "missing credential environment reference")
	}
}

func validate(data []byte, allowJobs bool) int {
	decoder := kyaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	count := 0
	for {
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err == io.EOF {
			break
		} else if err != nil {
			panic(err)
		}
		if len(raw) == 0 || string(raw) == "null" {
			continue
		}
		var header struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name         string `json:"name"`
				GenerateName string `json:"generateName"`
				Namespace    string `json:"namespace"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal(raw, &header); err != nil {
			panic(err)
		}
		require(header.Metadata.Namespace == "mansiflix", "unexpected namespace")
		switch header.Kind {
		case "Deployment":
			var d appsv1.Deployment
			strict(raw, &d)
			require(d.Name == "minio" && d.Spec.Replicas != nil && *d.Spec.Replicas == 1, "single MinIO replica required")
			require(d.Spec.Strategy.Type == appsv1.RecreateDeploymentStrategyType, "Recreate strategy required")
			checkPod(d.Spec.Template.Spec)
			c := d.Spec.Template.Spec.Containers[0]
			require(c.StartupProbe != nil && c.ReadinessProbe != nil && c.LivenessProbe != nil, "all three probes required")
		case "PersistentVolumeClaim":
			var p corev1.PersistentVolumeClaim
			strict(raw, &p)
			require(p.Name == "mansiflix-minio-data", "must create only the new MinIO PVC")
			require(p.Spec.StorageClassName != nil && *p.Spec.StorageClassName == "csi-cinder-sc-retain", "Retain class required")
			require(len(p.Spec.AccessModes) == 1 && p.Spec.AccessModes[0] == corev1.ReadWriteOnce, "RWO required")
		case "Service":
			var s corev1.Service
			strict(raw, &s)
			require(s.Spec.Type == corev1.ServiceTypeClusterIP && len(s.Spec.Ports) == 1 && s.Spec.Ports[0].Port == 9000, "internal S3-only service required")
			require(len(s.Spec.ExternalIPs) == 0 && s.Spec.Ports[0].NodePort == 0, "external S3 exposure is not approved")
			require(s.Spec.Selector["app.kubernetes.io/name"] == "minio" && s.Spec.Selector["app.kubernetes.io/part-of"] == "mansiflix", "unexpected Service selector")
		case "NetworkPolicy":
			var n networkv1.NetworkPolicy
			strict(raw, &n)
			require(n.Spec.PodSelector.MatchLabels["app.kubernetes.io/name"] == "minio" && n.Spec.PodSelector.MatchLabels["app.kubernetes.io/part-of"] == "mansiflix", "unexpected NetworkPolicy target")
			require(len(n.Spec.PolicyTypes) == 1 && n.Spec.PolicyTypes[0] == networkv1.PolicyTypeIngress, "unexpected policy types")
			require(len(n.Spec.Ingress) == 1 && len(n.Spec.Ingress[0].From) == 1, "one restricted ingress peer required")
			ports := n.Spec.Ingress[0].Ports
			require(len(ports) == 1 && ports[0].Port != nil && ports[0].Port.IntVal == 9000 && ports[0].Protocol != nil && *ports[0].Protocol == corev1.ProtocolTCP, "only TCP/9000 is approved")
			peer := n.Spec.Ingress[0].From[0]
			require(peer.NamespaceSelector == nil && peer.IPBlock == nil && peer.PodSelector != nil, "same-namespace peer required")
			require(peer.PodSelector.MatchLabels["mansiflix.local/minio-access"] == "true" && peer.PodSelector.MatchLabels["app.kubernetes.io/part-of"] == "mansiflix", "both client labels required")
		case "ConfigMap":
			var c corev1.ConfigMap
			strict(raw, &c)
			require(c.Name == "mansiflix-minio-tools", "unexpected ConfigMap")
			for _, name := range []string{"common.sh", "bootstrap.sh", "smoke-test.sh", "worker.json"} {
				require(c.Data[name] != "", "missing script or policy in ConfigMap")
			}
		case "Job":
			require(allowJobs, "base must not execute bootstrap or smoke tests")
			var j batchv1.Job
			strict(raw, &j)
			require(j.GenerateName != "" && j.Name == "", "one-shot Jobs need unique generated names")
			roles := map[string]string{
				"mansiflix-minio-bootstrap-": "bootstrap",
				"mansiflix-minio-smoke-":     "smoke-test",
				"mansiflix-minio-seed-":      "seed",
				"mansiflix-minio-hls-":       "upload",
				"mansiflix-minio-verify-":    "verify",
				"mansiflix-minio-network-":   "network",
			}
			role, reviewed := roles[j.GenerateName]
			require(reviewed && len(j.Spec.Template.Spec.Containers) == 1 && j.Spec.Template.Spec.Containers[0].Name == role, "only bootstrap/audit Jobs may receive both identities")
			require(j.Spec.ActiveDeadlineSeconds != nil && *j.Spec.ActiveDeadlineSeconds > 0 && j.Spec.BackoffLimit != nil && *j.Spec.BackoffLimit == 0, "bounded one-shot Job required")
			require(j.Spec.Template.Labels["mansiflix.local/minio-access"] == "true", "Job not admitted by NetworkPolicy")
			checkPod(j.Spec.Template.Spec)
			for _, v := range j.Spec.Template.Spec.Volumes {
				require(v.PersistentVolumeClaim == nil, "client Jobs must never mount data PVCs")
			}
		default:
			panic("unexpected resource kind: " + header.Kind)
		}
		count++
		fmt.Printf("OK %-24s %s%s\n", header.Kind, header.Metadata.Name, header.Metadata.GenerateName)
	}
	return count
}

func main() {
	require(len(os.Args) == 2, "usage: validate-local.go path/to/deploy/minio")
	root, err := filepath.Abs(os.Args[1])
	if err != nil {
		panic(err)
	}
	data, err := exec.Command("kubectl", "kustomize", root).Output()
	if err != nil {
		panic(err)
	}
	require(validate(data, false) == 5, "expected five base resources")
	for _, file := range []string{"bootstrap-job.yaml", "smoke-test-job.yaml", "seed-demo-job.yaml", "hls-demo-job.yaml", "verify-demo-job.yaml", "network-test-job.yaml"} {
		data, err = os.ReadFile(filepath.Join(root, file))
		if err != nil {
			panic(err)
		}
		require(validate(data, true) == 1, "expected one Job per file")
	}
	fmt.Println("PASS: offline strict typed decoding and review invariants (not Kubernetes server admission)")
}
