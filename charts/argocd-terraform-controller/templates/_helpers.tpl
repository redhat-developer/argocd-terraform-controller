{{- define "atc.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "atc.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "atc.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: {{ include "atc.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "atc.selectorLabels" -}}
app.kubernetes.io/name: {{ include "atc.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
control-plane: controller-manager
{{- end -}}

{{- define "atc.serviceAccountName" -}}
{{ include "atc.fullname" . }}
{{- end -}}

{{- define "atc.image" -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end -}}

{{- define "atc.runnerImage" -}}
{{ .Values.runner.image.repository }}:{{ .Values.runner.image.tag | default .Chart.AppVersion }}
{{- end -}}

{{- define "atc.runnerNamespaces" -}}
{{- $ns := list .Release.Namespace -}}
{{- range .Values.runner.namespaces -}}
{{- if not (has . $ns) -}}{{- $ns = append $ns . -}}{{- end -}}
{{- end -}}
{{- toJson $ns -}}
{{- end -}}

{{/* Argo CD integration helpers */}}
{{- define "atc.argocd.enabled" -}}
{{- if and .Values.argocd.integration.enabled (or .Values.argocd.integration.customizations.enabled .Values.argocd.integration.uiExtension.enabled) -}}true{{- end -}}
{{- end -}}

{{- define "atc.argocd.hookName" -}}
{{ include "atc.fullname" . }}-argocd-integration
{{- end -}}

{{- define "atc.argocd.extensionVersion" -}}
{{- $v := default .Chart.AppVersion .Values.argocd.integration.uiExtension.version -}}
{{- if hasPrefix "v" $v }}{{ $v }}{{ else }}v{{ $v }}{{ end -}}
{{- end -}}

{{- define "atc.argocd.extensionURL" -}}
{{- default (printf "https://github.com/redhat-developer/argocd-terraform-controller/releases/download/%s/argocd-terraform-extension.tar.gz" (include "atc.argocd.extensionVersion" .)) .Values.argocd.integration.uiExtension.extensionURL -}}
{{- end -}}

{{- define "atc.argocd.checksumURL" -}}
{{- default (printf "https://github.com/redhat-developer/argocd-terraform-controller/releases/download/%s/checksums.txt" (include "atc.argocd.extensionVersion" .)) .Values.argocd.integration.uiExtension.checksumURL -}}
{{- end -}}

{{/* Labels for hook resources; hooks are not tracked by the release so they
     carry the standard labels plus a component marker. */}}
{{- define "atc.argocd.hookLabels" -}}
{{ include "atc.labels" . }}
app.kubernetes.io/component: argocd-integration
{{- end -}}

{{/* Environment shared by the integration hook Jobs. */}}
{{- define "atc.argocd.hookEnv" -}}
{{- $i := .Values.argocd.integration -}}
- name: HOME
  value: /tmp
- name: KUBECACHEDIR
  value: /tmp/kube-cache
- name: ARGOCD_NAMESPACE
  value: {{ .Values.argocd.namespace | quote }}
- name: REQUIRED
  value: {{ $i.required | quote }}
- name: CUSTOMIZATIONS_ENABLED
  value: {{ $i.customizations.enabled | quote }}
- name: CONFIGMAP_NAME
  value: {{ $i.customizations.configMapName | quote }}
- name: UI_EXTENSION_ENABLED
  value: {{ $i.uiExtension.enabled | quote }}
- name: DEPLOYMENT_NAME
  value: {{ $i.uiExtension.deploymentName | quote }}
- name: CONTAINER_NAME
  value: {{ $i.uiExtension.containerName | quote }}
- name: INSTALLER_IMAGE
  value: {{ $i.uiExtension.installer.image | quote }}
- name: EXTENSION_URL
  value: {{ include "atc.argocd.extensionURL" . | quote }}
- name: EXTENSION_CHECKSUM_URL
  value: {{ include "atc.argocd.checksumURL" . | quote }}
- name: WAIT_FOR_ROLLOUT
  value: {{ $i.uiExtension.installer.waitForRollout | quote }}
- name: ROLLOUT_TIMEOUT
  value: {{ $i.uiExtension.installer.rolloutTimeout | quote }}
{{- end -}}

{{/* Render-time preflight.
     lookup only works against a live cluster (install/upgrade, dry-run=server);
     under `helm template`/`helm lint` it returns nothing, so first check for a
     namespace every cluster has to tell the two situations apart. */}}
{{- define "atc.argocd.clusterReachable" -}}
{{- if lookup "v1" "Namespace" "" "kube-system" }}true{{ end -}}
{{- end -}}

{{- define "atc.argocd.namespaceExists" -}}
{{- if or (not (include "atc.argocd.clusterReachable" .)) (lookup "v1" "Namespace" "" .Values.argocd.namespace) }}true{{ end -}}
{{- end -}}

{{/* Anything that needs objects in the Argo CD namespace. */}}
{{- define "atc.argocd.needed" -}}
{{- if or (include "atc.argocd.enabled" .) .Values.argocd.repoCredentials.enabled }}true{{ end -}}
{{- end -}}

{{/* Fails the render with an actionable message when Argo CD is required but
     its namespace is missing; otherwise returns "true" when Argo CD-namespaced
     objects may be rendered. */}}
{{- define "atc.argocd.ready" -}}
{{- if include "atc.argocd.needed" . -}}
  {{- if include "atc.argocd.namespaceExists" . -}}
true
  {{- else if .Values.argocd.integration.required -}}
    {{- fail (printf "\n\nArgo CD namespace %q not found.\n\n  - Argo CD installed elsewhere?  --set argocd.namespace=<namespace>\n  - Install without Argo CD?      --set argocd.integration.required=false\n  - Skip the integration?         --set argocd.integration.enabled=false --set argocd.repoCredentials.enabled=false\n" .Values.argocd.namespace) -}}
  {{- end -}}
{{- end -}}
{{- end -}}
