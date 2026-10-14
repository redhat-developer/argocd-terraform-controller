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

package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"text/template"
	"unicode"

	"github.com/hashicorp/terraform-exec/tfexec"
	"github.com/zclconf/go-cty/cty"
	ctyjson "github.com/zclconf/go-cty/cty/json"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	infrav1 "github.com/redhat-developer/argocd-terraform-controller/api/v1alpha1"
	"github.com/redhat-developer/argocd-terraform-controller/internal/runner"
)

// TypeInfoSuffix marks the companion key holding the cty type of a
// non-string output written to the outputs Secret.
const TypeInfoSuffix = "__type"

// --- outputs ---

func (r *TerraformReconciler) outputsMayBeDrifted(ctx context.Context, tf *infrav1.Terraform) (bool, error) {
	wots := tf.Spec.WriteOutputsToSecret
	if wots == nil {
		return false, nil
	}
	secret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Namespace: tf.Namespace, Name: wots.Name}, secret)
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	inSecret := make([]string, 0, len(secret.Data))
	for k := range secret.Data {
		if !strings.HasSuffix(k, TypeInfoSuffix) {
			inSecret = append(inSecret, k)
		}
	}
	want := make([]string, 0, len(wots.Outputs))
	for _, o := range wots.Outputs {
		_, alias := splitOutputMapping(o)
		want = append(want, alias)
	}
	if len(want) == 0 {
		want = append(want, tf.Status.AvailableOutputs...)
	}
	slices.Sort(inSecret)
	slices.Sort(want)
	return strings.Join(inSecret, ",") != strings.Join(want, ","), nil
}

func (r *TerraformReconciler) processOutputs(ctx context.Context, p *patcher, rc runner.RunnerClient, tf *infrav1.Terraform, s *session, revision string) error {
	reply, err := rc.Output(ctx, &runner.OutputRequest{TfInstance: s.instance})
	if err != nil {
		infrav1.TerraformNotReady(tf, revision, infrav1.TFExecOutputFailedReason, fmt.Sprintf("error running output: %s", err))
		return fmt.Errorf("error running output: %w", err)
	}
	outputs := make(map[string]tfexec.OutputMeta, len(reply.Outputs))
	names := make([]string, 0, len(reply.Outputs))
	for k, v := range reply.Outputs {
		outputs[k] = tfexec.OutputMeta{Sensitive: v.Sensitive, Type: v.Type, Value: v.Value}
		names = append(names, k)
	}
	if len(names) > 0 {
		slices.Sort(names)
		infrav1.TerraformOutputsAvailable(tf, names, "Outputs available")
	}

	if tf.Spec.WriteOutputsToSecret == nil || len(outputs) == 0 {
		return nil
	}
	if err := r.writeOutputs(ctx, tf, rc, outputs, revision); err != nil {
		return err
	}
	return p.Patch(ctx, tf)
}

func splitOutputMapping(m string) (output, alias string) {
	if o, a, ok := strings.Cut(m, ":"); ok && o != "" && a != "" {
		return o, a
	}
	return m, m
}

func filterOutputs(outputs map[string]tfexec.OutputMeta, selected []string) (map[string]tfexec.OutputMeta, error) {
	out := make(map[string]tfexec.OutputMeta, len(selected))
	for _, m := range selected {
		if m == "" {
			return nil, errors.New("output mapping cannot be empty")
		}
		name, alias := splitOutputMapping(m)
		meta, ok := outputs[name]
		if !ok {
			return nil, fmt.Errorf("output not found: %s", name)
		}
		out[alias] = meta
	}
	return out, nil
}

func (r *TerraformReconciler) writeOutputs(ctx context.Context, tf *infrav1.Terraform, rc runner.RunnerClient, outputs map[string]tfexec.OutputMeta, revision string) error {
	wots := tf.Spec.WriteOutputsToSecret
	selected := outputs
	if len(wots.Outputs) > 0 {
		var err error
		if selected, err = filterOutputs(outputs, wots.Outputs); err != nil {
			infrav1.TerraformNotReady(tf, revision, infrav1.OutputsWritingFailedReason, err.Error())
			return err
		}
	}

	data := make(map[string][]byte, len(selected))
	for name, meta := range selected {
		ct, err := ctyjson.UnmarshalType(meta.Type)
		if err != nil {
			return fmt.Errorf("output %s: %w", name, err)
		}
		if ct == cty.String {
			cv, err := ctyjson.Unmarshal(meta.Value, ct)
			if err != nil {
				return fmt.Errorf("output %s: %w", name, err)
			}
			data[name] = []byte(cv.AsString())
			continue
		}
		data[name] = meta.Value
		data[name+TypeInfoSuffix] = meta.Type
	}

	if len(data) == 0 || tf.Spec.Destroy {
		infrav1.TerraformOutputsWritten(tf, revision, "No outputs written")
		return nil
	}
	reply, err := rc.WriteOutputs(ctx, &runner.WriteOutputsRequest{
		Namespace: tf.Namespace, Name: tf.Name, SecretName: wots.Name, Uuid: string(tf.UID),
		Data: data, Labels: wots.Labels, Annotations: wots.Annotations,
	})
	if err != nil {
		infrav1.TerraformNotReady(tf, revision, infrav1.OutputsWritingFailedReason, err.Error())
		return err
	}
	if reply.Changed {
		keys := make([]string, 0, len(data))
		for k := range data {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		r.Eventf(tf, corev1.EventTypeNormal, infrav1.OutputsWrittenReason, "Outputs written.\n%d output(s): %s", len(keys), strings.Join(keys, ", "))
	}
	infrav1.TerraformOutputsWritten(tf, revision, "Outputs written")
	return nil
}

// --- health checks ---

func (r *TerraformReconciler) shouldDoHealthChecks(tf *infrav1.Terraform) bool {
	if len(tf.Spec.HealthChecks) == 0 {
		return false
	}
	hc := tf.GetCondition(infrav1.ConditionTypeHealthCheck)
	if hc != nil && hc.Reason == infrav1.HealthChecksFailedReason {
		return true
	}
	apply := tf.GetCondition(infrav1.ConditionTypeApply)
	return apply != nil && apply.Reason == infrav1.TFExecApplySucceedReason && hc == nil
}

func (r *TerraformReconciler) doHealthChecks(ctx context.Context, tf *infrav1.Terraform, rc runner.RunnerClient) error {
	outputs := map[string]string{}
	if tf.Spec.WriteOutputsToSecret != nil && tf.Spec.WriteOutputsToSecret.Name != "" {
		reply, err := rc.GetOutputs(ctx, &runner.GetOutputsRequest{Namespace: tf.Namespace, SecretName: tf.Spec.WriteOutputsToSecret.Name})
		if err != nil {
			err = fmt.Errorf("error getting outputs for health checks: %w", err)
			infrav1.TerraformHealthCheckFailed(tf, err.Error())
			return err
		}
		outputs = reply.Outputs
	}

	for _, hc := range tf.Spec.HealthChecks {
		var err error
		switch hc.Type {
		case infrav1.HealthCheckTypeTCP:
			var addr string
			if addr, err = renderHealthCheck(outputs, hc.Address); err == nil {
				err = tcpHealthCheck(hc.Name, addr, hc)
			}
		case infrav1.HealthCheckTypeHTTPGet:
			var u string
			if u, err = renderHealthCheck(outputs, hc.URL); err == nil {
				err = httpHealthCheck(ctx, hc.Name, u, hc)
			}
		}
		if err != nil {
			r.Eventf(tf, corev1.EventTypeWarning, infrav1.HealthChecksFailedReason, "Health check %q failed: %s", hc.Name, err)
			infrav1.TerraformHealthCheckFailed(tf, err.Error())
			return err
		}
	}
	ctrl.LoggerFrom(ctx).Info("health checks succeeded")
	infrav1.TerraformHealthCheckSucceeded(tf, "Health checks succeeded")
	return nil
}

func renderHealthCheck(outputs map[string]string, text string) (string, error) {
	tmpl, err := template.New("hc").Delims("{{", "}}").Option("missingkey=error").Parse(text)
	if err != nil {
		return "", fmt.Errorf("invalid health check template %q: %w", text, err)
	}
	var b bytes.Buffer
	if err := tmpl.Execute(&b, outputs); err != nil {
		return "", fmt.Errorf("unable to render health check %q: %w", text, err)
	}
	return b.String(), nil
}

func isValidLabel(label string) bool {
	if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
		return false
	}
	for _, ch := range label {
		if !unicode.IsLetter(ch) && !unicode.IsDigit(ch) && ch != '-' {
			return false
		}
	}
	return true
}

func validateTCPAddress(address string) error {
	if strings.Contains(address, "://") {
		return errors.New("URL schemes are not allowed")
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	if net.ParseIP(host) == nil {
		for label := range strings.SplitSeq(host, ".") {
			if !isValidLabel(label) {
				return errors.New("invalid host format")
			}
		}
	}
	if p, err := strconv.Atoi(port); err != nil || p <= 0 || p > 65535 {
		return errors.New("invalid port number")
	}
	return nil
}

func tcpHealthCheck(name, address string, hc infrav1.HealthCheck) error {
	if err := validateTCPAddress(address); err != nil {
		return fmt.Errorf("invalid address %q for tcp health check %s: %w", address, name, err)
	}
	conn, err := net.DialTimeout("tcp", address, hc.GetTimeout())
	if err != nil {
		return fmt.Errorf("tcp health check %s on %s failed: %w", name, address, err)
	}
	return conn.Close()
}

func httpHealthCheck(ctx context.Context, name, rawURL string, hc infrav1.HealthCheck) error {
	if _, err := url.ParseRequestURI(rawURL); err != nil {
		return fmt.Errorf("invalid url %q for http health check %s: %w", rawURL, name, err)
	}
	ctx, cancel := context.WithTimeout(ctx, hc.GetTimeout())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("http health check %s on %s failed: %w", name, rawURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusBadRequest {
		return nil
	}
	return fmt.Errorf("http health check %s on %s returned %d: %s", name, rawURL, resp.StatusCode, strings.TrimSpace(string(body)))
}
