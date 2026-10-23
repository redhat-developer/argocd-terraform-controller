#!/bin/sh
# Integrates the Argo CD Terraform Controller with an existing Argo CD
# installation, or removes that integration. Run by the Helm hook Jobs.
#
#   integrate.sh apply    post-install / post-upgrade
#   integrate.sh remove   pre-delete
#
# Argo CD's objects are patched in place because they belong to the Argo CD
# installation, not to this chart:
#   - ConfigMap  $CONFIGMAP_NAME   health assessment + resource actions (Lua)
#   - Deployment $DEPLOYMENT_NAME  argocd-extension-installer init container
set -eu

MODE="${1:-apply}"
PATCH_DIR=/scripts
TMP=/tmp

log()  { echo "[argocd-integration] $*"; }
fail() { echo "[argocd-integration] ERROR: $*" >&2; exit 1; }

# skip_or_fail MESSAGE: honour REQUIRED. When Argo CD is optional, exit 0 so
# the controller still installs without the integration.
skip_or_fail() {
  if [ "$REQUIRED" = "true" ]; then
    fail "$* (set argocd.integration.required=false to install without the Argo CD integration, or argocd.integration.enabled=false to skip it)"
  fi
  log "WARNING: $* - skipping the Argo CD integration"
  exit 0
}

# --- preflight ---------------------------------------------------------------

log "mode=$MODE namespace=$ARGOCD_NAMESPACE"

if ! kubectl get namespace "$ARGOCD_NAMESPACE" >/dev/null 2>&1; then
  if [ "$MODE" = "remove" ]; then log "namespace $ARGOCD_NAMESPACE not found, nothing to clean up"; exit 0; fi
  skip_or_fail "Argo CD namespace '$ARGOCD_NAMESPACE' not found (set argocd.namespace if Argo CD runs elsewhere)"
fi

if [ "$CUSTOMIZATIONS_ENABLED" = "true" ] && ! kubectl -n "$ARGOCD_NAMESPACE" get configmap "$CONFIGMAP_NAME" >/dev/null 2>&1; then
  if [ "$MODE" = "remove" ]; then log "ConfigMap $CONFIGMAP_NAME not found, skipping"; CUSTOMIZATIONS_ENABLED=false; else
  skip_or_fail "ConfigMap '$CONFIGMAP_NAME' not found in '$ARGOCD_NAMESPACE' - is Argo CD installed there?"; fi
fi

if [ "$UI_EXTENSION_ENABLED" = "true" ] && ! kubectl -n "$ARGOCD_NAMESPACE" get deployment "$DEPLOYMENT_NAME" >/dev/null 2>&1; then
  if [ "$MODE" = "remove" ]; then log "Deployment $DEPLOYMENT_NAME not found, skipping"; UI_EXTENSION_ENABLED=false; else
  skip_or_fail "Deployment '$DEPLOYMENT_NAME' not found in '$ARGOCD_NAMESPACE' - is Argo CD installed there?"; fi
fi

# Resolve the server container: explicit value, or the first container.
if [ "$UI_EXTENSION_ENABLED" = "true" ]; then
  if [ -z "$CONTAINER_NAME" ]; then
    CONTAINER_NAME="$(kubectl -n "$ARGOCD_NAMESPACE" get deployment "$DEPLOYMENT_NAME" -o jsonpath='{.spec.template.spec.containers[0].name}')"
    [ -n "$CONTAINER_NAME" ] || fail "could not determine the container name of Deployment $DEPLOYMENT_NAME"
    log "detected server container: $CONTAINER_NAME"
  elif ! kubectl -n "$ARGOCD_NAMESPACE" get deployment "$DEPLOYMENT_NAME" -o jsonpath='{.spec.template.spec.containers[*].name}' | tr ' ' '\n' | grep -qx "$CONTAINER_NAME"; then
    fail "container '$CONTAINER_NAME' not found in Deployment $DEPLOYMENT_NAME (set argocd.integration.uiExtension.containerName to one of: $(kubectl -n "$ARGOCD_NAMESPACE" get deployment "$DEPLOYMENT_NAME" -o jsonpath='{.spec.template.spec.containers[*].name}'))"
  fi
fi

# --- apply --------------------------------------------------------------------

apply_customizations() {
  log "merging health assessment and resource actions into ConfigMap $CONFIGMAP_NAME"
  # The generated patch is a full ConfigMap document; only its data matters.
  kubectl -n "$ARGOCD_NAMESPACE" patch configmap "$CONFIGMAP_NAME" --type merge \
    --patch "$(kubectl create -f "$PATCH_DIR/argocd-cm-patch.yaml" --dry-run=client -o json | jq '{data: .data}')"
}

apply_ui_extension() {
  log "installing the UI extension into Deployment $DEPLOYMENT_NAME (container $CONTAINER_NAME)"
  cat > "$TMP/server-patch.yaml" <<EOF
spec:
  template:
    spec:
      initContainers:
        - name: extension-terraform
          image: ${INSTALLER_IMAGE}
          env:
            - name: EXTENSION_NAME
              value: terraform
            - name: EXTENSION_URL
              value: ${EXTENSION_URL}
            - name: EXTENSION_CHECKSUM_URL
              value: ${EXTENSION_CHECKSUM_URL}
          volumeMounts:
            - name: extensions
              mountPath: /tmp/extensions/
          securityContext:
            runAsUser: 1000
            allowPrivilegeEscalation: false
      containers:
        - name: ${CONTAINER_NAME}
          volumeMounts:
            - name: extensions
              mountPath: /tmp/extensions/
      volumes:
        - name: extensions
          emptyDir: {}
EOF
  kubectl -n "$ARGOCD_NAMESPACE" patch deployment "$DEPLOYMENT_NAME" --patch-file "$TMP/server-patch.yaml"
  if [ "$WAIT_FOR_ROLLOUT" = "true" ]; then
    log "waiting for $DEPLOYMENT_NAME to roll out (timeout $ROLLOUT_TIMEOUT)"
    kubectl -n "$ARGOCD_NAMESPACE" rollout status "deployment/$DEPLOYMENT_NAME" --timeout="$ROLLOUT_TIMEOUT"
  fi
}

# --- remove -------------------------------------------------------------------

remove_customizations() {
  log "removing health assessment and resource actions from ConfigMap $CONFIGMAP_NAME"
  # Build a JSON patch that removes only the keys we own and that exist.
  keys="$(kubectl create -f "$PATCH_DIR/argocd-cm-patch.yaml" --dry-run=client -o json | jq -r '.data | keys[]')"
  existing="$(kubectl -n "$ARGOCD_NAMESPACE" get configmap "$CONFIGMAP_NAME" -o json | jq -r '.data // {} | keys[]')"
  ops=""
  for k in $keys; do
    if echo "$existing" | grep -qxF "$k"; then
      esc="$(printf '%s' "$k" | sed 's#~#~0#g; s#/#~1#g')"
      ops="${ops}${ops:+,}{\"op\":\"remove\",\"path\":\"/data/${esc}\"}"
    fi
  done
  if [ -z "$ops" ]; then log "nothing to remove from $CONFIGMAP_NAME"; return; fi
  kubectl -n "$ARGOCD_NAMESPACE" patch configmap "$CONFIGMAP_NAME" --type json --patch "[$ops]"
}

remove_ui_extension() {
  log "removing the UI extension from Deployment $DEPLOYMENT_NAME (container $CONTAINER_NAME)"
  if ! kubectl -n "$ARGOCD_NAMESPACE" get deployment "$DEPLOYMENT_NAME" -o jsonpath='{.spec.template.spec.initContainers[*].name}' | tr ' ' '\n' | grep -qx extension-terraform; then
    log "init container extension-terraform not present, nothing to remove"
    return
  fi
  cat > "$TMP/server-unpatch.yaml" <<EOF
spec:
  template:
    spec:
      initContainers:
        - name: extension-terraform
          \$patch: delete
      volumes:
        - name: extensions
          \$patch: delete
      containers:
        - name: ${CONTAINER_NAME}
          volumeMounts:
            - mountPath: /tmp/extensions/
              \$patch: delete
EOF
  kubectl -n "$ARGOCD_NAMESPACE" patch deployment "$DEPLOYMENT_NAME" --patch-file "$TMP/server-unpatch.yaml"
}

# --- main ---------------------------------------------------------------------

case "$MODE" in
  apply)
    [ "$CUSTOMIZATIONS_ENABLED" = "true" ] && apply_customizations
    [ "$UI_EXTENSION_ENABLED" = "true" ] && apply_ui_extension
    log "Argo CD integration complete. Hard-refresh the Argo CD UI to load the extension."
    ;;
  remove)
    [ "$UI_EXTENSION_ENABLED" = "true" ] && remove_ui_extension
    [ "$CUSTOMIZATIONS_ENABLED" = "true" ] && remove_customizations
    log "Argo CD integration removed."
    ;;
  *)
    fail "unknown mode '$MODE' (expected apply|remove)"
    ;;
esac
