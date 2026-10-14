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
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/hashicorp/terraform-exec/tfexec"
	"google.golang.org/protobuf/protoadapt"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

// backendConfigOptions resolves spec.backendConfigsFrom into -backend-config
// options.
func (r *Server) backendConfigOptions(ctx context.Context) ([]tfexec.InitOption, error) {
	tf := r.terraform
	var opts []tfexec.InitOption
	add := func(k string, v []byte) {
		opts = append(opts, tfexec.BackendConfig(k+"="+string(v)))
	}

	for _, ref := range tf.Spec.BackendConfigsFrom {
		key := types.NamespacedName{Namespace: tf.Namespace, Name: ref.Name}
		switch ref.Kind {
		case "Secret":
			var s corev1.Secret
			if err := r.Get(ctx, key, &s); err != nil {
				if ref.Optional {
					continue
				}
				return nil, fmt.Errorf("unable to get backend config Secret %s: %w", key, err)
			}
			if ref.Keys == nil {
				for k, v := range s.Data {
					add(k, v)
				}
			} else {
				for _, k := range ref.Keys {
					if v, ok := s.Data[k]; ok {
						add(k, v)
					}
				}
			}
		case "ConfigMap":
			var cm corev1.ConfigMap
			if err := r.Get(ctx, key, &cm); err != nil {
				if ref.Optional {
					continue
				}
				return nil, fmt.Errorf("unable to get backend config ConfigMap %s: %w", key, err)
			}
			if ref.Keys == nil {
				for k, v := range cm.Data {
					add(k, []byte(v))
				}
				for k, v := range cm.BinaryData {
					add(k, v)
				}
			} else {
				for _, k := range ref.Keys {
					if v, ok := cm.Data[k]; ok {
						add(k, []byte(v))
					}
					if v, ok := cm.BinaryData[k]; ok {
						add(k, v)
					}
				}
			}
		}
	}
	return opts, nil
}

// quietInit runs init capturing stderr so the error carries the real
// terraform diagnostics instead of just the exit code.
func (r *Server) quietInit(ctx context.Context, opts ...tfexec.InitOption) error {
	log := r.log(ctx)
	r.tf.SetStdout(nil)
	errBuf := &bytes.Buffer{}
	r.tf.SetStderr(errBuf)
	defer r.initLogger(log)

	err := r.tf.Init(ctx, opts...)
	var lockErr *StateLockError
	if err != nil && !errors.As(err, &lockErr) && errBuf.Len() > 0 {
		fmt.Fprint(os.Stderr, errBuf.String())
		err = errors.New(errBuf.String())
	}
	return err
}

// Init runs terraform init with backend configs.
func (r *Server) Init(ctx context.Context, req *InitRequest) (*InitReply, error) {
	if err := r.validateInstanceID(req.TfInstance); err != nil {
		return nil, err
	}
	ctx, cancel := r.cancelOnDone(ctx)
	defer cancel()

	backendOpts, err := r.backendConfigOptions(ctx)
	if err != nil {
		return nil, err
	}
	opts := append([]tfexec.InitOption{tfexec.Upgrade(req.Upgrade), tfexec.ForceCopy(req.ForceCopy)}, backendOpts...)

	if err := r.quietInit(ctx, opts...); err != nil {
		r.log(ctx).Error(err, "init failed")
		return nil, lockErrStatus(err, func(id string) protoadapt.MessageV1 {
			return &InitReply{Message: replyNotOK, StateLockIdentifier: id}
		})
	}
	return &InitReply{Message: "ok"}, nil
}
