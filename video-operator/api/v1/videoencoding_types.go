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

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// VideoEncodingSpec defines the desired state of VideoEncoding
type VideoEncodingSpec struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Source string `json:"source"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:items:Enum=360;720;1080
	Resolutions []int32 `json:"resolutions"`
}

// Phases shared by the encoding and each resolution.
const (
	PhasePending    = "Pending"
	PhaseProcessing = "Processing"
	PhaseCompleted  = "Completed"
	PhaseFailed     = "Failed"
)

// ResolutionStatus records the observed state of a child Job.
type ResolutionStatus struct {
	Resolution int32  `json:"resolution"`
	Phase      string `json:"phase"`
	JobName    string `json:"jobName,omitempty"`
}

// VideoEncodingStatus defines the observed state of VideoEncoding.
type VideoEncodingStatus struct {
	Phase       string             `json:"phase,omitempty"`
	Resolutions []ResolutionStatus `json:"resolutions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// VideoEncoding is the Schema for the videoencodings API
type VideoEncoding struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of VideoEncoding
	// +required
	Spec VideoEncodingSpec `json:"spec"`

	// status defines the observed state of VideoEncoding
	// +optional
	Status VideoEncodingStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// VideoEncodingList contains a list of VideoEncoding
type VideoEncodingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []VideoEncoding `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &VideoEncoding{}, &VideoEncodingList{})
		return nil
	})
}
