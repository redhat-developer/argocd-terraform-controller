-- Health assessment for infra.argocd.io/Terraform.
--
-- Healthy     : Ready=True (applied, no drift, outputs written)
-- Progressing : waiting for the controller, planning, applying, or a
--               dependency is not ready yet
-- Suspended   : spec.suspend, or a plan is waiting for manual approval. A
--               Suspended resource holds up later sync waves, so Applications
--               that depend on the infrastructure wait for the approval.
-- Degraded    : the last run failed, drift was detected, the state is locked,
--               or the object is stalled
local hs = {}

if obj.spec ~= nil and obj.spec.suspend == true then
  hs.status = "Suspended"
  hs.message = "Reconciliation is suspended"
  return hs
end

if obj.status == nil or obj.status.conditions == nil then
  hs.status = "Progressing"
  hs.message = "Waiting for the Terraform controller"
  return hs
end

local ready, stalled
for _, c in ipairs(obj.status.conditions) do
  if c.type == "Ready" then
    ready = c
  elseif c.type == "Stalled" then
    stalled = c
  end
end

if stalled ~= nil and stalled.status == "True" then
  hs.status = "Degraded"
  hs.message = stalled.message
  return hs
end

if ready == nil then
  hs.status = "Progressing"
  hs.message = "Waiting for the Terraform controller"
  return hs
end

if ready.status == "True" then
  hs.status = "Healthy"
  hs.message = ready.message
  return hs
end

if ready.status == "False" then
  if ready.reason == "DependencyNotReady" then
    hs.status = "Progressing"
  elseif ready.reason == "Suspended" then
    hs.status = "Suspended"
  else
    hs.status = "Degraded"
  end
  hs.message = ready.message
  return hs
end

-- Argo CD's Lua sandbox opens only the base, table and (safe) os libraries,
-- so there is no string.sub: p is a prefix of s iff p <= s < p .. "\255"
-- under byte-wise comparison (valid UTF-8 never contains the byte 0xFF).
local function has_prefix(s, p)
  return p ~= nil and p ~= "" and p <= s and s < p .. "\255"
end

-- Ready=Unknown: either in progress or waiting for a human.
local pending = ""
if obj.status.plan ~= nil and obj.status.plan.pending ~= nil then
  pending = obj.status.plan.pending
end
local spec = obj.spec or {}
local approved = spec.force == true or spec.approvePlan == "auto" or has_prefix(pending, spec.approvePlan)

if pending ~= "" and ready.reason == "TerraformPlannedWithChanges" then
  if spec.planOnly == true then
    hs.status = "Healthy"
    hs.message = "Plan " .. pending .. " generated (plan only mode)"
    return hs
  end
  if not approved then
    hs.status = "Suspended"
    hs.message = "Plan " .. pending .. " is waiting for approval"
    return hs
  end
end

hs.status = "Progressing"
hs.message = ready.message
return hs
