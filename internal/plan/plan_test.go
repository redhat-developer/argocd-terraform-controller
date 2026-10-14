/*
Copyright 2026 The Argo CD Terraform Controller Authors.

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

package plan

import (
	"bytes"
	"crypto/rand"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestSafeLabelValue(t *testing.T) {
	short := "my-plan"
	if got := SafeLabelValue(short); got != short {
		t.Errorf("short value changed: %q", got)
	}
	long := strings.Repeat("a", 100)
	got := SafeLabelValue(long)
	if len(got) != 63 {
		t.Errorf("len = %d, want 63", len(got))
	}
	if got == SafeLabelValue(strings.Repeat("a", 99)+"b") {
		t.Error("different long values must not collide")
	}
}

func TestSecretRoundTrip(t *testing.T) {
	data := []byte("binary plan contents")
	p := New("stack", "ns", "default", "uid-1", "plan-main-abc", data)

	secrets, err := p.ToSecrets("")
	if err != nil {
		t.Fatal(err)
	}
	if len(secrets) != 1 {
		t.Fatalf("expected 1 secret, got %d", len(secrets))
	}
	s := secrets[0]
	if s.Name != "tfplan-default-stack" {
		t.Errorf("name = %q", s.Name)
	}
	if s.Annotations[EncodingAnnotation] != "gzip" || s.Annotations[SavedAnnotation] != "plan-main-abc" {
		t.Errorf("annotations = %v", s.Annotations)
	}
	if s.Labels[NameLabel] != "stack" || s.Labels[WorkspaceLabel] != "default" {
		t.Errorf("labels = %v", s.Labels)
	}
	if len(s.OwnerReferences) != 1 || s.OwnerReferences[0].Kind != "Terraform" {
		t.Errorf("ownerRefs = %v", s.OwnerReferences)
	}

	items := make([]corev1.Secret, 0, len(secrets))
	for _, s := range secrets {
		items = append(items, *s)
	}
	back, err := FromSecrets("stack", "ns", "uid-1", items)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(back.Bytes(), data) || back.ID() != "plan-main-abc" {
		t.Errorf("round trip mismatch: %q %q", back.Bytes(), back.ID())
	}
}

func TestChunkedSecrets(t *testing.T) {
	// Random data does not compress, so it must be chunked.
	data := make([]byte, 2*maxChunkBytes+10)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	p := New("big", "ns", "dev", "uid", "plan-x", data)
	secrets, err := p.ToSecrets("")
	if err != nil {
		t.Fatal(err)
	}
	if len(secrets) < 2 {
		t.Fatalf("expected chunking, got %d secrets", len(secrets))
	}
	for i, s := range secrets {
		if s.Annotations[ChunkAnnotation] == "" {
			t.Errorf("chunk %d missing chunk annotation", i)
		}
		if len(s.Data[DataKey]) > maxChunkBytes {
			t.Errorf("chunk %d too large: %d", i, len(s.Data[DataKey]))
		}
	}

	// Reassemble out of order.
	items := make([]corev1.Secret, 0, len(secrets))
	for _, s := range slices.Backward(secrets) {
		items = append(items, *s)
	}
	back, err := FromSecrets("big", "ns", "uid", items)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(back.Bytes(), data) {
		t.Error("chunked round trip mismatch")
	}
}

func TestConfigMapRoundTrip(t *testing.T) {
	text := "Plan: 1 to add, 0 to change, 0 to destroy."
	p := New("stack", "ns", "default", "uid", "plan-1", []byte(text))
	cms := p.ToConfigMaps("-readable")
	if len(cms) != 1 || cms[0].Name != "tfplan-default-stack-readable" {
		t.Fatalf("unexpected configmaps: %+v", cms)
	}
	if cms[0].Data[DataKey] != text {
		t.Errorf("data = %q", cms[0].Data[DataKey])
	}
	back, err := FromConfigMaps("stack", "ns", "uid", []corev1.ConfigMap{*cms[0]})
	if err != nil {
		t.Fatal(err)
	}
	if string(back.Bytes()) != text {
		t.Errorf("round trip = %q", back.Bytes())
	}
}

func TestFromSecretsMissingChunk(t *testing.T) {
	data := make([]byte, maxChunkBytes+100)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	secrets, _ := New("n", "ns", "default", "u", "p", data).ToSecrets("")
	if len(secrets) < 2 {
		t.Skip("data compressed into a single chunk")
	}
	if _, err := FromSecrets("n", "ns", "u", []corev1.Secret{*secrets[1]}); err == nil {
		t.Error("expected error for missing chunk 0")
	}
}
