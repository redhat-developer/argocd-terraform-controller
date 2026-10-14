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

// Package plan encodes Terraform plans into (possibly chunked) Kubernetes
// Secrets and ConfigMaps and reconstructs them.
package plan

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	infrav1 "github.com/redhat-developer/argocd-terraform-controller/api/v1alpha1"
)

const (
	// NameLabel and WorkspaceLabel identify every object belonging to a plan.
	NameLabel      = "infra.argocd.io/plan-name"
	WorkspaceLabel = "infra.argocd.io/plan-workspace"

	FullNameAnnotation      = "infra.argocd.io/plan-full-name"
	FullWorkspaceAnnotation = "infra.argocd.io/plan-full-workspace"
	ChunkAnnotation         = "infra.argocd.io/plan-chunk"
	HashAnnotation          = "infra.argocd.io/plan-hash"
	// SavedAnnotation holds the plan ID.
	SavedAnnotation    = "savedPlan"
	EncodingAnnotation = "encoding"

	// DataKey is the key holding the plan in Secrets and ConfigMaps.
	DataKey = "tfplan"

	// maxChunkBytes keeps every object under the 1MiB etcd limit.
	maxChunkBytes = 1 * 1024 * 1024
)

// SafeLabelValue truncates value to a valid label value, appending a hash to
// keep it unique.
func SafeLabelValue(value string) string {
	if len(value) <= 63 {
		return value
	}
	checksum := sha256.Sum256([]byte(value))
	suffix := fmt.Sprintf("-%x", checksum[:8])
	return value[:63-len(suffix)] + suffix
}

// Plan is a plan payload together with the metadata needed to store it.
type Plan struct {
	name      string
	namespace string
	workspace string
	uid       string
	planID    string
	bytes     []byte
}

// New creates a Plan from raw bytes.
func New(name, namespace, workspace, uid, planID string, data []byte) *Plan {
	return &Plan{name: name, namespace: namespace, workspace: workspace, uid: uid, planID: planID, bytes: data}
}

// Bytes returns the plan payload.
func (p *Plan) Bytes() []byte { return p.bytes }

// ID returns the plan ID.
func (p *Plan) ID() string { return p.planID }

// ObjectName returns the base object name for this plan.
func ObjectName(workspace, name, suffix string) string {
	return fmt.Sprintf("tfplan-%s-%s", workspace, name+suffix)
}

// Labels returns the selector labels for the plan objects of name/workspace.
func Labels(name, workspace, suffix string) map[string]string {
	return map[string]string{
		NameLabel:      SafeLabelValue(name + suffix),
		WorkspaceLabel: SafeLabelValue(workspace),
	}
}

func (p *Plan) ownerRef() metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion: infrav1.GroupVersion.String(),
		Kind:       infrav1.TerraformKind,
		Name:       p.name,
		UID:        types.UID(p.uid),
	}
}

func (p *Plan) meta(suffix string, chunk int, total int, payload []byte) metav1.ObjectMeta {
	name := ObjectName(p.workspace, p.name, suffix)
	if total > 1 {
		name = fmt.Sprintf("%s-%d", name, chunk)
	}
	ann := map[string]string{
		FullNameAnnotation:      p.name + suffix,
		FullWorkspaceAnnotation: p.workspace,
		SavedAnnotation:         p.planID,
		HashAnnotation:          fmt.Sprintf("%x", sha256.Sum256(payload)),
	}
	if total > 1 {
		ann[ChunkAnnotation] = strconv.Itoa(chunk)
	}
	return metav1.ObjectMeta{
		Name:            name,
		Namespace:       p.namespace,
		Annotations:     ann,
		Labels:          Labels(p.name, p.workspace, suffix),
		OwnerReferences: []metav1.OwnerReference{p.ownerRef()},
	}
}

func chunks(data []byte) [][]byte {
	if len(data) <= maxChunkBytes {
		return [][]byte{data}
	}
	var out [][]byte
	for start := 0; start < len(data); start += maxChunkBytes {
		out = append(out, data[start:min(start+maxChunkBytes, len(data))])
	}
	return out
}

// ToSecrets gzip-encodes the plan and splits it into Secrets.
func (p *Plan) ToSecrets(suffix string) ([]*corev1.Secret, error) {
	encoded, err := GzipEncode(p.bytes)
	if err != nil {
		return nil, fmt.Errorf("unable to gzip encode the plan: %w", err)
	}
	parts := chunks(encoded)
	secrets := make([]*corev1.Secret, 0, len(parts))
	for i, part := range parts {
		m := p.meta(suffix, i, len(parts), part)
		m.Annotations[EncodingAnnotation] = "gzip"
		secrets = append(secrets, &corev1.Secret{
			ObjectMeta: m,
			Type:       corev1.SecretTypeOpaque,
			Data:       map[string][]byte{DataKey: part},
		})
	}
	return secrets, nil
}

// ToConfigMaps splits the (plain text) plan into ConfigMaps.
func (p *Plan) ToConfigMaps(suffix string) []*corev1.ConfigMap {
	parts := chunks(p.bytes)
	cms := make([]*corev1.ConfigMap, 0, len(parts))
	for i, part := range parts {
		cms = append(cms, &corev1.ConfigMap{
			ObjectMeta: p.meta(suffix, i, len(parts), part),
			Data:       map[string]string{DataKey: string(part)},
		})
	}
	return cms
}

type chunkSource interface {
	GetName() string
	GetAnnotations() map[string]string
	GetLabels() map[string]string
}

func chunkIndex(obj chunkSource) (int, error) {
	idx, ok := obj.GetAnnotations()[ChunkAnnotation]
	if !ok || idx == "" {
		return 0, nil
	}
	i, err := strconv.Atoi(idx)
	if err != nil {
		return 0, fmt.Errorf("invalid chunk index annotation on %s: %w", obj.GetName(), err)
	}
	return i, nil
}

func workspaceAndID(obj chunkSource) (string, string, error) {
	ws, ok := obj.GetAnnotations()[FullWorkspaceAnnotation]
	if !ok {
		if ws, ok = obj.GetLabels()[WorkspaceLabel]; !ok {
			return "", "", fmt.Errorf("missing plan workspace label and annotation on %s", obj.GetName())
		}
	}
	id, ok := obj.GetAnnotations()[SavedAnnotation]
	if !ok {
		return "", "", fmt.Errorf("missing plan ID annotation on %s", obj.GetName())
	}
	return ws, id, nil
}

func assemble(name string, chunkMap map[int][]byte) ([]byte, error) {
	var out []byte
	for i := 0; i < len(chunkMap); i++ {
		c, ok := chunkMap[i]
		if !ok {
			return nil, fmt.Errorf("missing chunk %d for terraform %s", i, name)
		}
		out = append(out, c...)
	}
	return out, nil
}

// FromSecrets reconstructs a Plan from its Secrets.
func FromSecrets(name, namespace, uid string, secrets []corev1.Secret) (*Plan, error) {
	chunkMap := make(map[int][]byte, len(secrets))
	var workspace, planID string
	for i := range secrets {
		s := &secrets[i]
		data, ok := s.Data[DataKey]
		if !ok {
			return nil, fmt.Errorf("secret %s missing key %s", s.Name, DataKey)
		}
		idx, err := chunkIndex(s)
		if err != nil {
			return nil, err
		}
		if workspace, planID, err = workspaceAndID(s); err != nil {
			return nil, err
		}
		chunkMap[idx] = data
	}
	encoded, err := assemble(name, chunkMap)
	if err != nil {
		return nil, err
	}
	data, err := GzipDecode(encoded)
	if err != nil {
		return nil, fmt.Errorf("failed to decode plan for %s: %w", name, err)
	}
	return New(name, namespace, workspace, uid, planID, data), nil
}

// FromConfigMaps reconstructs a Plan from its ConfigMaps.
func FromConfigMaps(name, namespace, uid string, cms []corev1.ConfigMap) (*Plan, error) {
	chunkMap := make(map[int][]byte, len(cms))
	var workspace, planID string
	for i := range cms {
		cm := &cms[i]
		data, ok := cm.Data[DataKey]
		if !ok {
			return nil, fmt.Errorf("configmap %s missing key %s", cm.Name, DataKey)
		}
		idx, err := chunkIndex(cm)
		if err != nil {
			return nil, err
		}
		if workspace, planID, err = workspaceAndID(cm); err != nil {
			return nil, err
		}
		chunkMap[idx] = []byte(data)
	}
	data, err := assemble(name, chunkMap)
	if err != nil {
		return nil, err
	}
	return New(name, namespace, workspace, uid, planID, data), nil
}

// GzipEncode compresses data.
func GzipEncode(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// GzipDecode decompresses data.
func GzipDecode(data []byte) ([]byte, error) {
	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer func() { _ = gr.Close() }()
	return io.ReadAll(gr)
}
