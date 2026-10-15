-- Request an immediate reconciliation. When a plan is waiting for approval
-- the controller discards it and plans again from the current source.
--
-- Argo CD's Lua sandbox exposes os only as a module (see the built-in
-- Deployment restart action), not as a global.
local os = require("os")
if obj.metadata.annotations == nil then
  obj.metadata.annotations = {}
end
obj.metadata.annotations["infra.argocd.io/reconcile"] = os.date("!%Y-%m-%dT%XZ")
return obj
