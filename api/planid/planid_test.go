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

package planid

import "testing"

func TestGetPlanID(t *testing.T) {
	tests := []struct {
		name     string
		revision string
		want     string
	}{
		{"ref@algo:long hash", "main@sha1:12345678901234567890", "plan-main-1234567890"},
		{"ref@algo:exact hash", "main@sha1:1234567890", "plan-main-1234567890"},
		{"ref@algo:short hash", "main@sha1:123456789", "plan-main-123456789"},
		{"ref with slash is sanitised", "feature/x@sha1:abcdefabcdefabcdef", "plan-feature-x-abcdefabcd"},
		{"ref@hash without algo", "main@abcdefabcdefabcdef", "plan-main-abcdefabcd"},
		{"legacy ref/hash", "main/abcdefabcdefabcdef", "plan-main-abcdefabcd"},
		{"bare long hash", "abcdefabcdefabcdef", "plan-abcdefabcd"},
		{"bare short hash", "abc", "plan-abc"},
		{"tag", "v1.2.3@sha1:0123456789abcdef", "plan-v1.2.3-0123456789"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GetPlanID(tt.revision); got != tt.want {
				t.Errorf("GetPlanID(%q) = %q, want %q", tt.revision, got, tt.want)
			}
		})
	}
}

func TestMatches(t *testing.T) {
	tests := []struct {
		approve string
		planID  string
		want    bool
	}{
		{"plan-main-1234567890", "plan-main-1234567890", true},
		{"plan-main-123", "plan-main-1234567890", true},
		{"plan-main", "plan-main-1234567890", true},
		{"plan-other-1234567890", "plan-main-1234567890", false},
		{"", "plan-main-1234567890", false},
		{"plan-main-1234567890", "", false},
		{"auto", "plan-main-1234567890", false},
	}
	for _, tt := range tests {
		if got := Matches(tt.approve, tt.planID); got != tt.want {
			t.Errorf("Matches(%q, %q) = %v, want %v", tt.approve, tt.planID, got, tt.want)
		}
	}
}

func TestGetApproveMessage(t *testing.T) {
	got := GetApproveMessage("plan-main-abc", "Plan generated")
	want := `Plan generated: set approvePlan: "plan-main-abc" to approve this plan.`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
