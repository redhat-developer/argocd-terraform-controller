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

// Package argocd tests the Lua resource customizations shipped in
// config/argocd/lua the way Argo CD evaluates them: the resource is exposed
// as the global `obj` and the script returns a table.
package argocd

import (
	"os"
	"path/filepath"
	"testing"

	lua "github.com/yuin/gopher-lua"
	"sigs.k8s.io/yaml"
)

var luaDir = filepath.Join("..", "..", "config", "argocd", "lua")

// run evaluates script with obj (YAML) as the global `obj` and returns the
// returned table as a Go value.
func run(t *testing.T, script, objYAML string) map[string]any {
	t.Helper()
	var obj map[string]any
	if err := yaml.Unmarshal([]byte(objYAML), &obj); err != nil {
		t.Fatalf("invalid fixture: %v", err)
	}
	// Mirror Argo CD's sandbox (util/lua/lua.go): only package, base and
	// table are opened, and os is available solely through require("os").
	// Notably there is no string library and no os global.
	L := lua.NewState(lua.Options{SkipOpenLibs: true})
	defer L.Close()
	for _, pair := range []struct {
		n string
		f lua.LGFunction
	}{{lua.LoadLibName, lua.OpenPackage}, {lua.BaseLibName, lua.OpenBase}, {lua.TabLibName, lua.OpenTable}} {
		if err := L.CallByParam(lua.P{Fn: L.NewFunction(pair.f), NRet: 0, Protect: true}, lua.LString(pair.n)); err != nil {
			t.Fatal(err)
		}
	}
	L.PreloadModule(lua.OsLibName, lua.OpenOs)
	L.SetGlobal("obj", toLua(L, obj))
	if err := L.DoString(script); err != nil {
		t.Fatalf("lua error: %v", err)
	}
	ret := L.Get(-1)
	table, ok := ret.(*lua.LTable)
	if !ok {
		t.Fatalf("script returned %T, want table", ret)
	}
	out, _ := fromLua(table).(map[string]any)
	return out
}

func toLua(L *lua.LState, v any) lua.LValue {
	switch val := v.(type) {
	case nil:
		return lua.LNil
	case bool:
		return lua.LBool(val)
	case float64:
		return lua.LNumber(val)
	case int64:
		return lua.LNumber(val)
	case string:
		return lua.LString(val)
	case []any:
		t := L.NewTable()
		for _, item := range val {
			t.Append(toLua(L, item))
		}
		return t
	case map[string]any:
		t := L.NewTable()
		for k, item := range val {
			t.RawSetString(k, toLua(L, item))
		}
		return t
	}
	return lua.LString("unsupported")
}

func fromLua(v lua.LValue) any {
	switch val := v.(type) {
	case *lua.LTable:
		if val.MaxN() > 0 {
			arr := make([]any, 0, val.MaxN())
			val.ForEach(func(_, item lua.LValue) { arr = append(arr, fromLua(item)) })
			return arr
		}
		m := map[string]any{}
		val.ForEach(func(k, item lua.LValue) { m[k.String()] = fromLua(item) })
		return m
	case lua.LBool:
		return bool(val)
	case lua.LNumber:
		return float64(val)
	case lua.LString:
		return string(val)
	}
	return nil
}

func load(t *testing.T, parts ...string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(append([]string{luaDir}, parts...)...))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func dig(m map[string]any, path ...string) any {
	var cur any = m
	for _, p := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[p]
	}
	return cur
}

const base = `
apiVersion: infra.argocd.io/v1alpha1
kind: Terraform
metadata:
  name: stack
  namespace: default
spec:
  interval: 1m
  source:
    git:
      url: https://example.com/repo.git
`

func TestHealth(t *testing.T) {
	health := load(t, "health.lua")
	tests := []struct {
		name       string
		obj        string
		wantStatus string
		wantSubstr string
	}{
		{"no status", base, "Progressing", "Waiting"},
		{"suspended spec", base + "  suspend: true\n", "Suspended", "suspended"},
		{"ready", base + `
status:
  conditions:
  - type: Ready
    status: "True"
    reason: TerraformOutputsWritten
    message: "Outputs written: main@sha1:abc"
`, "Healthy", "Outputs written"},
		{"progressing", base + `
status:
  conditions:
  - type: Ready
    status: "Unknown"
    reason: Progressing
    message: Initializing
`, "Progressing", "Initializing"},
		{"pending manual approval", base + `
status:
  plan:
    pending: plan-main-abc123
  conditions:
  - type: Ready
    status: "Unknown"
    reason: TerraformPlannedWithChanges
    message: "Plan generated: set approvePlan ..."
`, "Suspended", "plan-main-abc123"},
		{"pending but approved", base + `  approvePlan: plan-main-abc123
status:
  plan:
    pending: plan-main-abc123
  conditions:
  - type: Ready
    status: "Unknown"
    reason: TerraformPlannedWithChanges
    message: "Plan generated"
`, "Progressing", "Plan generated"},
		{"pending approved by prefix", base + `  approvePlan: plan-main-abc
status:
  plan:
    pending: plan-main-abc123
  conditions:
  - type: Ready
    status: "Unknown"
    reason: TerraformPlannedWithChanges
    message: "Plan generated"
`, "Progressing", "Plan generated"},
		{"pending with non-matching approvePlan", base + `  approvePlan: plan-main-abd
status:
  plan:
    pending: plan-main-abc123
  conditions:
  - type: Ready
    status: "Unknown"
    reason: TerraformPlannedWithChanges
    message: "Plan generated"
`, "Suspended", "plan-main-abc123"},
		{"pending auto", base + `  approvePlan: auto
status:
  plan:
    pending: plan-main-abc123
  conditions:
  - type: Ready
    status: "Unknown"
    reason: TerraformPlannedWithChanges
    message: "Plan generated"
`, "Progressing", ""},
		{"plan only", base + `  planOnly: true
status:
  plan:
    pending: plan-main-abc123
  conditions:
  - type: Ready
    status: "Unknown"
    reason: TerraformPlannedWithChanges
    message: "Plan generated"
`, "Healthy", "plan only"},
		{"failed", base + `
status:
  conditions:
  - type: Ready
    status: "False"
    reason: TFExecApplyFailed
    message: "error running apply"
`, "Degraded", "error running apply"},
		{"drift", base + `
status:
  conditions:
  - type: Ready
    status: "False"
    reason: DriftDetected
    message: "~ resource changed"
`, "Degraded", "resource changed"},
		{"dependency not ready", base + `
status:
  conditions:
  - type: Ready
    status: "False"
    reason: DependencyNotReady
    message: "dependency default/base is not ready"
`, "Progressing", "dependency"},
		{"stalled", base + `
status:
  conditions:
  - type: Ready
    status: "False"
    reason: SourceFetchFailed
    message: "git credentials Secret not found"
  - type: Stalled
    status: "True"
    reason: SourceFetchFailed
    message: "git credentials Secret not found"
`, "Degraded", "Secret not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hs := run(t, health, tt.obj)
			if hs["status"] != tt.wantStatus {
				t.Errorf("status = %v, want %v (message %q)", hs["status"], tt.wantStatus, hs["message"])
			}
			if msg, _ := hs["message"].(string); tt.wantSubstr != "" && !contains(msg, tt.wantSubstr) {
				t.Errorf("message %q does not contain %q", msg, tt.wantSubstr)
			}
		})
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestActionDiscovery(t *testing.T) {
	discovery := load(t, "actions", "discovery.lua")

	pending := base + `
status:
  plan:
    pending: plan-main-abc123
  lock:
    pending: ""
`
	acts := run(t, discovery, pending)
	for name, wantDisabled := range map[string]bool{
		"approve-plan": false, "replan": false, "suspend": false, "resume": true, "force-unlock": true,
	} {
		if got := dig(acts, name, "disabled"); got != wantDisabled {
			t.Errorf("%s disabled = %v, want %v", name, got, wantDisabled)
		}
	}
	if dig(acts, "approve-plan", "displayName") != "Approve Plan" {
		t.Errorf("missing displayName: %v", acts["approve-plan"])
	}

	approved := run(t, discovery, base+"  approvePlan: auto\nstatus:\n  plan:\n    pending: plan-x\n")
	if dig(approved, "approve-plan", "disabled") != true {
		t.Error("approve-plan must be disabled when approvePlan is auto")
	}

	// approvePlan matches the pending plan by prefix, like the controller.
	for approve, want := range map[string]bool{
		"plan-main-abc123": true, "plan-main-abc": true, "plan-main": true, "plan-main-abc124": false, "plan-main-abc1234": false, "plan-mai": true, "plan-x": false, "": false,
	} {
		got := run(t, discovery, base+"  approvePlan: "+approve+"\nstatus:\n  plan:\n    pending: plan-main-abc123\n")
		if dig(got, "approve-plan", "disabled") != want {
			t.Errorf("approvePlan=%q: approve-plan disabled = %v, want %v", approve, dig(got, "approve-plan", "disabled"), want)
		}
	}

	locked := run(t, discovery, base+"  suspend: true\nstatus:\n  lock:\n    pending: lock-1\n")
	if dig(locked, "force-unlock", "disabled") != false || dig(locked, "resume", "disabled") != false || dig(locked, "suspend", "disabled") != true {
		t.Errorf("unexpected actions when suspended and locked: %v", locked)
	}
}

func TestActions(t *testing.T) {
	pending := base + "\nstatus:\n  plan:\n    pending: plan-main-abc123\n"

	obj := run(t, load(t, "actions", "approve-plan", "action.lua"), pending)
	if dig(obj, "spec", "approvePlan") != "plan-main-abc123" {
		t.Errorf("approve-plan: %v", dig(obj, "spec"))
	}

	obj = run(t, load(t, "actions", "replan", "action.lua"), pending)
	if v, _ := dig(obj, "metadata", "annotations", "infra.argocd.io/reconcile").(string); len(v) < 10 {
		t.Errorf("replan annotation = %q", v)
	}

	obj = run(t, load(t, "actions", "suspend", "action.lua"), base)
	if dig(obj, "spec", "suspend") != true {
		t.Error("suspend did not set spec.suspend")
	}
	obj = run(t, load(t, "actions", "resume", "action.lua"), base+"  suspend: true\n")
	if dig(obj, "spec", "suspend") != false {
		t.Error("resume did not clear spec.suspend")
	}

	obj = run(t, load(t, "actions", "force-unlock", "action.lua"), base)
	if dig(obj, "spec", "tfstate", "forceUnlock") != "auto" {
		t.Errorf("force-unlock: %v", dig(obj, "spec", "tfstate"))
	}
}
