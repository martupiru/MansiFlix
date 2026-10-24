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
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestGetJobPhase(t *testing.T) {
	tests := []struct {
		name   string
		status batchv1.JobStatus
		want   string
	}{
		{name: "Pending", want: videov1.PhasePending},
		{name: "Processing", status: batchv1.JobStatus{Active: 1}, want: videov1.PhaseProcessing},
		{name: "Completed", status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}}, want: videov1.PhaseCompleted},
		{name: "Failed", status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}}}, want: videov1.PhaseFailed},
		{name: "False terminal condition", status: batchv1.JobStatus{Active: 1, Conditions: []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionFalse}}}, want: videov1.PhaseProcessing},
		{name: "Retrying failed Pod", status: batchv1.JobStatus{Failed: 1}, want: videov1.PhasePending},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := getJobPhase(&batchv1.Job{Status: tt.status}); got != tt.want {
				t.Fatalf("Got %s, want %s", got, tt.want)
			}
		})
	}
}

func TestGetEncodingPhase(t *testing.T) {
	tests := []struct {
		phases []string
		want   string
	}{
		{nil, videov1.PhasePending},
		{[]string{videov1.PhasePending, videov1.PhaseCompleted}, videov1.PhasePending},
		{[]string{videov1.PhaseCompleted, videov1.PhaseCompleted}, videov1.PhaseCompleted},
		{[]string{videov1.PhaseCompleted, videov1.PhaseProcessing}, videov1.PhaseProcessing},
		{[]string{videov1.PhaseProcessing, videov1.PhaseFailed}, videov1.PhaseFailed},
		{[]string{videov1.PhaseFailed, videov1.PhaseCompleted}, videov1.PhaseFailed},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprint(tt.phases), func(t *testing.T) {
			resolutions := make([]videov1.ResolutionStatus, len(tt.phases))
			for i, phase := range tt.phases {
				resolutions[i].Phase = phase
			}
			if got := getEncodingPhase(resolutions); got != tt.want {
				t.Fatalf("Got %s, want %s", got, tt.want)
			}
		})
	}
}

func TestReconcileStatusTransitions(t *testing.T) {
	ctx := context.Background()
	scheme := testEncodingScheme(t)
	encoding := &videov1.VideoEncoding{ObjectMeta: metav1.ObjectMeta{Name: testEncodingName, Namespace: testNamespace, UID: types.UID(testEncodingUID)}, Spec: videov1.VideoEncodingSpec{Source: testSource, Resolutions: []int32{360, 720, 1080}}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&videov1.VideoEncoding{}, &batchv1.Job{}).WithObjects(encoding).Build()
	reconciler := &VideoEncodingReconciler{Client: c, Scheme: scheme}
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(encoding)}
	reconcile := func(want string) {
		t.Helper()
		if _, err := reconciler.Reconcile(ctx, req); err != nil {
			t.Fatal(err)
		}
		if err := c.Get(ctx, req.NamespacedName, encoding); err != nil {
			t.Fatal(err)
		}
		if encoding.Status.Phase != want {
			t.Fatalf("Got status %+v, want %s", encoding.Status, want)
		}
		if len(encoding.Status.Resolutions) != 3 {
			t.Fatalf("Expected 3 resolutions: %+v", encoding.Status)
		}
		for i, resolution := range encoding.Spec.Resolutions {
			status := encoding.Status.Resolutions[i]
			if status.Resolution != resolution || status.JobName != fmt.Sprintf("matrix-%dp-job", resolution) {
				t.Fatalf("Unexpected resolution status: %+v", status)
			}
		}
	}
	setJobStatus := func(resolution int32, status batchv1.JobStatus) {
		t.Helper()
		job := &batchv1.Job{}
		key := types.NamespacedName{Name: fmt.Sprintf("matrix-%dp-job", resolution), Namespace: encoding.Namespace}
		if err := c.Get(ctx, key, job); err != nil {
			t.Fatal(err)
		}
		job.Status = status
		if err := c.Status().Update(ctx, job); err != nil {
			t.Fatal(err)
		}
	}
	reconcile(videov1.PhasePending)
	version := encoding.ResourceVersion
	reconcile(videov1.PhasePending)
	if encoding.ResourceVersion != version {
		t.Fatal("Unchanged status triggered an unnecessary update")
	}
	setJobStatus(720, batchv1.JobStatus{Active: 1})
	reconcile(videov1.PhaseProcessing)
	for _, resolution := range encoding.Spec.Resolutions {
		setJobStatus(resolution, batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}})
	}
	reconcile(videov1.PhaseCompleted)
	for _, status := range encoding.Status.Resolutions {
		if status.Phase != videov1.PhaseCompleted {
			t.Fatalf("Unexpected resolution phase: %+v", status)
		}
	}
	setJobStatus(360, batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}}})
	reconcile(videov1.PhaseFailed)
	// Removing a child recreates only that Job and returns it to Pending.
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "matrix-360p-job", Namespace: encoding.Namespace}}
	if err := c.Delete(ctx, job); err != nil {
		t.Fatal(err)
	}
	reconcile(videov1.PhasePending)
	if !reflect.DeepEqual(encoding.Status.Resolutions[0], videov1.ResolutionStatus{Resolution: 360, Phase: videov1.PhasePending, JobName: job.Name}) {
		t.Fatalf("Unexpected recreated Job status: %+v", encoding.Status)
	}
}
