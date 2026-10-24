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
	"reflect"
	"testing"

	videov1 "github.com/martupiru/MansiFlix/video-operator/api/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	testEncodingName = "matrix"
	testEncodingUID  = "matrix-uid"
	testSource       = "matrix.mp4"
	testNamespace    = "mansiflix"
)

func testEncodingScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, register := range []func(*runtime.Scheme) error{videov1.AddToScheme, batchv1.AddToScheme} {
		if err := register(scheme); err != nil {
			t.Fatal(err)
		}
	}
	return scheme
}

func TestBuildEncodingJob(t *testing.T) {
	scheme := testEncodingScheme(t)
	encoding := &videov1.VideoEncoding{ObjectMeta: metav1.ObjectMeta{Name: testEncodingName, Namespace: testNamespace, UID: types.UID(testEncodingUID)}, Spec: videov1.VideoEncodingSpec{Source: testSource, Resolutions: []int32{360, 720, 1080}}}
	for _, resolution := range encoding.Spec.Resolutions {
		t.Run(fmt.Sprint(resolution), func(t *testing.T) {
			job := buildEncodingJob(encoding, resolution)
			if job.Name != fmt.Sprintf("matrix-%dp-job", resolution) || job.Namespace != encoding.Namespace {
				t.Fatalf("Unexpected Job metadata: %+v", job.ObjectMeta)
			}
			pod := job.Spec.Template.Spec
			if pod.RestartPolicy != corev1.RestartPolicyNever {
				t.Fatalf("Unexpected restart policy: %s", pod.RestartPolicy)
			}
			if len(pod.Containers) != 1 {
				t.Fatalf("Expected one container, got %d", len(pod.Containers))
			}
			container := pod.Containers[0]
			if container.Image != workerImage {
				t.Fatalf("Unexpected image: %s", container.Image)
			}
			if len(container.Command) != 0 || len(container.Args) != 0 {
				t.Fatalf("Expected image entrypoint, got %v %v", container.Command, container.Args)
			}
			expected := []corev1.EnvVar{
				{Name: "SOURCE", Value: "/videos/originals/matrix.mp4"},
				{Name: "RESOLUTION", Value: fmt.Sprint(resolution)},
				{Name: "OUTPUT", Value: fmt.Sprintf("/videos/encoded/matrix/%d", resolution)},
			}
			if !reflect.DeepEqual(container.Env, expected) {
				t.Fatalf("Unexpected env: %+v", container.Env)
			}
			if !reflect.DeepEqual(container.VolumeMounts, []corev1.VolumeMount{{Name: "videos", MountPath: "/videos"}}) {
				t.Fatalf("Unexpected mounts: %+v", container.VolumeMounts)
			}
			if len(pod.Volumes) != 1 || pod.Volumes[0].Name != "videos" || pod.Volumes[0].PersistentVolumeClaim == nil || pod.Volumes[0].PersistentVolumeClaim.ClaimName != "mansiflix-videos" {
				t.Fatalf("Unexpected volumes: %+v", pod.Volumes)
			}
			if job.Spec.BackoffLimit == nil || *job.Spec.BackoffLimit != 2 || job.Spec.TTLSecondsAfterFinished != nil {
				t.Fatal("Expected two retries and retained Jobs")
			}
			if err := ctrl.SetControllerReference(encoding, job, scheme); err != nil {
				t.Fatal(err)
			}
			owner := metav1.GetControllerOf(job)
			if owner == nil || owner.UID != encoding.UID || owner.Kind != "VideoEncoding" || owner.APIVersion != videov1.GroupVersion.String() {
				t.Fatalf("Unexpected owner: %+v", owner)
			}
		})
	}
}

func TestReconcileJobsIdempotent(t *testing.T) {
	scheme := testEncodingScheme(t)
	encoding := &videov1.VideoEncoding{ObjectMeta: metav1.ObjectMeta{Name: testEncodingName, Namespace: testNamespace, UID: types.UID(testEncodingUID)}, Spec: videov1.VideoEncodingSpec{Source: testSource, Resolutions: []int32{360, 720, 1080}}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&videov1.VideoEncoding{}, &batchv1.Job{}).WithObjects(encoding).Build()
	reconciler := &VideoEncodingReconciler{Client: c, Scheme: scheme}
	ctx := context.Background()
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(encoding)}
	var first batchv1.JobList
	for attempt := range 2 {
		if _, err := reconciler.Reconcile(ctx, req); err != nil {
			t.Fatal(err)
		}
		var jobs batchv1.JobList
		if err := c.List(ctx, &jobs, client.InNamespace(encoding.Namespace)); err != nil {
			t.Fatal(err)
		}
		if len(jobs.Items) != 3 {
			t.Fatalf("Expected 3 Jobs, got %d", len(jobs.Items))
		}
		if attempt == 0 {
			first = jobs
		} else if !reflect.DeepEqual(first.Items, jobs.Items) {
			t.Fatal("Repeated reconciliation changed existing Jobs")
		}
	}
	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "missing", Namespace: encoding.Namespace}}); err != nil {
		t.Fatal(err)
	}
}
