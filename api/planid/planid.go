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

// Package planid derives stable, human-typeable plan identifiers from source
// revisions, and formats the approval hint shown to users.
package planid

import (
	"fmt"
	"strings"
)

const (
	prefix     = "plan-"
	hashLength = 10
)

// GetPlanID returns the plan ID for a revision of the form
// "<ref>@<algo>:<hash>" (e.g. "main@sha1:abc..."), falling back to the legacy
// "<ref>/<hash>" and bare "<hash>" forms. The hash is truncated to 10 chars.
func GetPlanID(revision string) string {
	if ref, after, ok := strings.Cut(revision, "@"); ok {
		hash := after
		if _, h, ok := strings.Cut(after, ":"); ok {
			hash = h
		}
		return prefix + sanitize(ref) + "-" + shortHash(hash)
	}

	if ref, hash, ok := strings.Cut(revision, "/"); ok && !strings.Contains(hash, "/") {
		return prefix + sanitize(ref) + "-" + shortHash(hash)
	}

	return prefix + shortHash(revision)
}

// GetApproveMessage formats the message shown to users who must approve planID.
func GetApproveMessage(planID string, message string) string {
	return fmt.Sprintf("%s: set approvePlan: %q to approve this plan.", message, planID)
}

// Matches reports whether approvePlan approves planID. Exact matches and
// prefix matches (so a user may paste the hint, or type only the ref part
// when it is unambiguous) are accepted.
func Matches(approvePlan, planID string) bool {
	if approvePlan == "" || planID == "" {
		return false
	}
	return approvePlan == planID || strings.HasPrefix(planID, approvePlan)
}

func shortHash(hash string) string {
	if len(hash) > hashLength {
		return hash[:hashLength]
	}
	return hash
}

// sanitize keeps ref names usable in a single spec field by replacing
// path separators, which appear in refs like "feature/x".
func sanitize(ref string) string {
	return strings.ReplaceAll(ref, "/", "-")
}
