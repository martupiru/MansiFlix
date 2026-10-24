/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"path"
	"reflect"
	"strings"
	"time"

	videov1 "github.com/martupiru/MansiFlix/video-operator/api/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const workerImage = "mansiflix/video-worker:dev"

// VideoEncodingReconciler reconciles a VideoEncoding object.
type VideoEncodingReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=video.mansiflix.local,resources=videoencodings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=video.mansiflix.local,resources=videoencodings/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=video.mansiflix.local,resources=videoencodings/finalizers,verbs=update
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete

// Reconcile creates missing Jobs and records their observed status.
func (r *VideoEncodingReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	var encoding videov1.VideoEncoding
	if err := r.Get(ctx, req.NamespacedName, &encoding); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !encoding.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	source := path.Clean(encoding.Spec.Source)
	if path.IsAbs(source) || source == ".." || strings.HasPrefix(source, "../") || source == "." {
		return ctrl.Result{}, fmt.Errorf("source must be a relative file path within /videos/originals: %q", encoding.Spec.Source)
	}
	status := videov1.VideoEncodingStatus{Resolutions: make([]videov1.ResolutionStatus, 0, len(encoding.Spec.Resolutions))}
	for _, resolution := range encoding.Spec.Resolutions {
		desired := buildEncodingJob(&encoding, resolution)
		var job batchv1.Job
		// Get + IsNotFound keeps reconciliation idempotent: existing Jobs are
		// preserved, and only missing names are created.
		err := r.Get(ctx, client.ObjectKeyFromObject(desired), &job)
		if apierrors.IsNotFound(err) {
			if err := ctrl.SetControllerReference(&encoding, desired, r.Scheme); err != nil {
				return ctrl.Result{}, err
			}
			if err := r.Create(ctx, desired); err != nil {
				return ctrl.Result{}, err
			}
			job = *desired
			logger.Info("Created Job", "job", job.Name, "resolution", resolution)
		} else if err != nil {
			return ctrl.Result{}, err
		} else if !metav1.IsControlledBy(&job, &encoding) {
			return ctrl.Result{}, fmt.Errorf("job %s/%s already exists and is not controlled by VideoEncoding %s", job.Namespace, job.Name, encoding.Name)
		}
		status.Resolutions = append(status.Resolutions, videov1.ResolutionStatus{Resolution: resolution, Phase: getJobPhase(&job), JobName: job.Name})
	}
	status.Phase = getEncodingPhase(status.Resolutions)
	// Re-fetch before updating to avoid overwriting concurrent spec/status edits.
	var latest videov1.VideoEncoding
	if err := r.Get(ctx, req.NamespacedName, &latest); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if latest.UID != encoding.UID || latest.Generation != encoding.Generation {
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}
	if !reflect.DeepEqual(latest.Status, status) {
		latest.Status = status
		if err := r.Status().Update(ctx, &latest); err != nil {
			return ctrl.Result{}, err
		}
		logger.Info("Updated VideoEncoding status", "name", encoding.Name, "phase", status.Phase)
	}
	return ctrl.Result{}, nil
}

// getJobPhase only treats terminal conditions with status True as terminal.
func getJobPhase(job *batchv1.Job) string {
	for _, condition := range job.Status.Conditions {
		if condition.Type == batchv1.JobFailed && condition.Status == corev1.ConditionTrue {
			return videov1.PhaseFailed
		}
	}
	for _, condition := range job.Status.Conditions {
		if condition.Type == batchv1.JobComplete && condition.Status == corev1.ConditionTrue {
			return videov1.PhaseCompleted
		}
	}
	if job.Status.Active > 0 {
		return videov1.PhaseProcessing
	}
	return videov1.PhasePending
}

func getEncodingPhase(resolutions []videov1.ResolutionStatus) string {
	completed := len(resolutions) > 0
	processing := false
	for _, resolution := range resolutions {
		if resolution.Phase == videov1.PhaseFailed {
			return videov1.PhaseFailed
		}
		completed = completed && resolution.Phase == videov1.PhaseCompleted
		processing = processing || resolution.Phase == videov1.PhaseProcessing
	}
	if completed {
		return videov1.PhaseCompleted
	}
	if processing {
		return videov1.PhaseProcessing
	}
	return videov1.PhasePending
}

// buildEncodingJob constructs a Job without submitting it to Kubernetes.
func buildEncodingJob(encoding *videov1.VideoEncoding, resolution int32) *batchv1.Job {
	backoffLimit := int32(2)
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-%dp-job", encoding.Name, resolution), Namespace: encoding.Namespace},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoffLimit,
			// Keep finished Jobs so the controller can inspect terminal conditions.
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				RestartPolicy: corev1.RestartPolicyNever,
				Containers: []corev1.Container{{
					Name: "encoder", Image: workerImage, ImagePullPolicy: corev1.PullIfNotPresent,
					Env: []corev1.EnvVar{
						{Name: "SOURCE", Value: path.Join("/videos/originals", encoding.Spec.Source)},
						{Name: "RESOLUTION", Value: fmt.Sprint(resolution)},
						{Name: "OUTPUT", Value: path.Join("/videos/encoded", encoding.Name, fmt.Sprint(resolution))},
					},
					VolumeMounts: []corev1.VolumeMount{{Name: "videos", MountPath: "/videos"}},
				}},
				Volumes: []corev1.Volume{{Name: "videos", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "mansiflix-videos"}}}},
			}},
		},
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *VideoEncodingReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&videov1.VideoEncoding{}).
		Owns(&batchv1.Job{}).
		Named("videoencoding").
		Complete(r)
}
