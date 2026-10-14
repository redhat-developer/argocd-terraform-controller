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

package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/Masterminds/sprig/v3"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	generatedVarsFile = "generated.auto.tfvars.json"
	mainTemplateFile  = "main.tf.tpl"
)

func jsonEncodeBytes(b []byte) (*apiextensionsv1.JSON, error) {
	data, err := json.Marshal(string(b))
	if err != nil {
		return nil, fmt.Errorf("could not encode bytes to json: %w", err)
	}
	return &apiextensionsv1.JSON{Raw: data}, nil
}

// parseRenamePattern splits "key:newName"; a bare key maps to itself.
func parseRenamePattern(pattern string) (string, string, error) {
	oldKey, newKey, ok := strings.Cut(pattern, ":")
	if !ok {
		return pattern, pattern, nil
	}
	if oldKey == "" || newKey == "" || strings.Contains(newKey, ":") {
		return "", "", fmt.Errorf("invalid rename pattern %q", pattern)
	}
	return oldKey, newKey, nil
}

// GenerateVarsForTF writes generated.auto.tfvars.json from spec.values,
// spec.vars and spec.varsFrom (later sources override earlier ones).
func (r *Server) GenerateVarsForTF(ctx context.Context, req *GenerateVarsForTFRequest) (*GenerateVarsForTFReply, error) {
	log := r.log(ctx)
	tf := r.terraform
	vars := map[string]*apiextensionsv1.JSON{}

	if tf.Spec.Values != nil {
		vars["values"] = &apiextensionsv1.JSON{Raw: tf.Spec.Values.Raw}
	}

	for _, v := range tf.Spec.Vars {
		switch {
		case v.ValueFrom != nil:
			val, err := r.resolveValueFrom(ctx, tf.Namespace, v.ValueFrom)
			if err != nil {
				return nil, fmt.Errorf("variable %q: %w", v.Name, err)
			}
			if val != nil {
				vars[v.Name] = val
			}
		case v.Value != nil:
			vars[v.Name] = v.Value
		}
	}

	for _, vf := range tf.Spec.VarsFrom {
		key := types.NamespacedName{Namespace: tf.Namespace, Name: vf.Name}
		var data map[string][]byte
		switch vf.Kind {
		case "Secret":
			var s corev1.Secret
			if err := r.Get(ctx, key, &s); err != nil {
				if vf.Optional {
					continue
				}
				return nil, fmt.Errorf("unable to get varsFrom Secret %s: %w", key, err)
			}
			data = s.Data
		case "ConfigMap":
			var cm corev1.ConfigMap
			if err := r.Get(ctx, key, &cm); err != nil {
				if vf.Optional {
					continue
				}
				return nil, fmt.Errorf("unable to get varsFrom ConfigMap %s: %w", key, err)
			}
			data = make(map[string][]byte, len(cm.Data)+len(cm.BinaryData))
			for k, v := range cm.Data {
				data[k] = []byte(v)
			}
			maps.Copy(data, cm.BinaryData)
		}

		if vf.VarsKeys == nil {
			for k, v := range data {
				enc, err := jsonEncodeBytes(v)
				if err != nil {
					return nil, fmt.Errorf("key %q: %w", k, err)
				}
				vars[k] = enc
			}
			continue
		}
		for _, pattern := range vf.VarsKeys {
			oldKey, newKey, err := parseRenamePattern(pattern)
			if err != nil {
				return nil, err
			}
			v, ok := data[oldKey]
			if !ok {
				continue
			}
			enc, err := jsonEncodeBytes(v)
			if err != nil {
				return nil, fmt.Errorf("key %q: %w", pattern, err)
			}
			vars[newKey] = enc
		}
	}

	out, err := json.Marshal(vars)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(req.WorkingDir, generatedVarsFile)
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return nil, fmt.Errorf("error writing %s: %w", generatedVarsFile, err)
	}
	log.Info("generated variables file", "path", path, "count", len(vars))
	return &GenerateVarsForTFReply{Message: "ok"}, nil
}

func (r *Server) resolveValueFrom(ctx context.Context, namespace string, src *corev1.EnvVarSource) (*apiextensionsv1.JSON, error) {
	switch {
	case src.SecretKeyRef != nil:
		var s corev1.Secret
		if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: src.SecretKeyRef.Name}, &s); err != nil {
			if src.SecretKeyRef.Optional != nil && *src.SecretKeyRef.Optional {
				return nil, nil
			}
			return nil, err
		}
		v, ok := s.Data[src.SecretKeyRef.Key]
		if !ok {
			if src.SecretKeyRef.Optional != nil && *src.SecretKeyRef.Optional {
				return nil, nil
			}
			return nil, fmt.Errorf("key %q not found in Secret %s", src.SecretKeyRef.Key, src.SecretKeyRef.Name)
		}
		return jsonEncodeBytes(v)
	case src.ConfigMapKeyRef != nil:
		var cm corev1.ConfigMap
		if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: src.ConfigMapKeyRef.Name}, &cm); err != nil {
			if src.ConfigMapKeyRef.Optional != nil && *src.ConfigMapKeyRef.Optional {
				return nil, nil
			}
			return nil, err
		}
		if v, ok := cm.Data[src.ConfigMapKeyRef.Key]; ok {
			return jsonEncodeBytes([]byte(v))
		}
		if v, ok := cm.BinaryData[src.ConfigMapKeyRef.Key]; ok {
			return jsonEncodeBytes(v)
		}
		if src.ConfigMapKeyRef.Optional != nil && *src.ConfigMapKeyRef.Optional {
			return nil, nil
		}
		return nil, fmt.Errorf("key %q not found in ConfigMap %s", src.ConfigMapKeyRef.Key, src.ConfigMapKeyRef.Name)
	}
	return nil, fmt.Errorf("valueFrom must set secretKeyRef or configMapKeyRef")
}

// GenerateTemplate renders main.tf.tpl (if present) to main.tf with the
// generated variables, using Helm-like syntax ({{ .Values.x }}).
func (r *Server) GenerateTemplate(ctx context.Context, req *GenerateTemplateRequest) (*GenerateTemplateReply, error) {
	log := r.log(ctx)
	tplPath := filepath.Join(req.WorkingDir, mainTemplateFile)
	if _, err := os.Stat(tplPath); os.IsNotExist(err) {
		return &GenerateTemplateReply{Message: "ok"}, nil
	}

	raw, err := os.ReadFile(filepath.Join(req.WorkingDir, generatedVarsFile))
	if err != nil {
		return nil, fmt.Errorf("unable to read %s: %w", generatedVarsFile, err)
	}
	vars := map[string]any{}
	if err := json.Unmarshal(raw, &vars); err != nil {
		return nil, err
	}
	vars["Values"] = vars["values"]

	tmpl, err := template.New(mainTemplateFile).Delims("{{", "}}").Funcs(sprig.TxtFuncMap()).ParseFiles(tplPath)
	if err != nil {
		return nil, fmt.Errorf("unable to parse %s: %w", mainTemplateFile, err)
	}
	f, err := os.Create(filepath.Join(req.WorkingDir, "main.tf"))
	if err != nil {
		return nil, err
	}
	if err := tmpl.Execute(f, vars); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("unable to render %s: %w", mainTemplateFile, err)
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	log.Info("rendered template", "template", tplPath)
	return &GenerateTemplateReply{Message: "ok"}, nil
}
