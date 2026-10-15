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

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	videov1 "github.com/martupiru/MansiFlix/video-operator/api/v1"
)

// VideoEncodingReconciler reconciles a VideoEncoding object
type VideoEncodingReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=video.mansiflix.local,resources=videoencodings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=video.mansiflix.local,resources=videoencodings/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=video.mansiflix.local,resources=videoencodings/finalizers,verbs=update
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the VideoEncoding object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.25.0/pkg/reconcile
func (r *VideoEncodingReconciler) Reconcile(
	ctx context.Context,
	req ctrl.Request,
) (ctrl.Result, error) {

	logger := log.FromContext(ctx)

	var encoding videov1.VideoEncoding

	// Buscar el VideoEncoding que disparó el reconcile
	if err := r.Get(
		ctx,
		req.NamespacedName,
		&encoding,
	); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	logger.Info(
		"VideoEncoding detectado",
		"name", encoding.Name,
		"source", encoding.Spec.Source,
		"resolution", encoding.Spec.Resolution,
	)

	jobName := encoding.Name + "-job"

	// Ver si el Job ya existe
	var existingJob batchv1.Job

	err := r.Get(
		ctx,
		types.NamespacedName{
			Name:      jobName,
			Namespace: encoding.Namespace,
		},
		&existingJob,
	)

	if err == nil {
		// El Job ya existe, no hacemos nada
		return ctrl.Result{}, nil
	}

	if !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}

	// Crear el Job
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: encoding.Namespace,
		},

		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{

					RestartPolicy: corev1.RestartPolicyNever,

					Containers: []corev1.Container{
						{
							Name:  "encoder",
							Image: "busybox:latest",

							Command: []string{
								"sh",
								"-c",
								fmt.Sprintf(
									"echo 'Procesando %s a %dp'",
									encoding.Spec.Source,
									encoding.Spec.Resolution,
								),
							},
						},
					},
				},
			},
		},
	}

	// Hacer que el VideoEncoding sea dueño del Job
	if err := ctrl.SetControllerReference(
		&encoding,
		job,
		r.Scheme,
	); err != nil {
		return ctrl.Result{}, err
	}

	logger.Info(
		"Creando Job",
		"job", jobName,
	)

	if err := r.Create(ctx, job); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *VideoEncodingReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&videov1.VideoEncoding{}).
		Owns(&batchv1.Job{}).
		Named("videoencoding").
		Complete(r)
}
