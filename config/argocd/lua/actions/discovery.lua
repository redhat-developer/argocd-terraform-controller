-- Actions available on infra.argocd.io/Terraform.
-- Argo CD's Lua sandbox opens only the base, table and (safe) os libraries,
-- so there is no string.sub: p is a prefix of s iff p <= s < p .. "\255"
-- under byte-wise comparison (valid UTF-8 never contains the byte 0xFF).
local function has_prefix(s, p)
  return p ~= nil and p ~= "" and p <= s and s < p .. "\255"
end
local actions = {}
local spec = obj.spec or {}
local status = obj.status or {}

local pending = ""
if status.plan ~= nil and status.plan.pending ~= nil then
  pending = status.plan.pending
end
local approved = spec.force == true or spec.approvePlan == "auto" or has_prefix(pending, spec.approvePlan)
local locked = status.lock ~= nil and status.lock.pending ~= nil and status.lock.pending ~= ""

actions["approve-plan"] = {
  ["displayName"] = "Approve Plan",
  ["iconClass"] = "fa fa-fw fa-check",
  ["disabled"] = pending == "" or approved or spec.planOnly == true or spec.suspend == true,
}
actions["replan"] = {
  ["displayName"] = "Re-plan",
  ["iconClass"] = "fa fa-fw fa-sync",
  ["disabled"] = spec.suspend == true,
}
actions["suspend"] = {
  ["displayName"] = "Suspend",
  ["iconClass"] = "fa fa-fw fa-pause",
  ["disabled"] = spec.suspend == true,
}
actions["resume"] = {
  ["displayName"] = "Resume",
  ["iconClass"] = "fa fa-fw fa-play",
  ["disabled"] = spec.suspend ~= true,
}
actions["force-unlock"] = {
  ["displayName"] = "Force Unlock State",
  ["iconClass"] = "fa fa-fw fa-unlock",
  ["disabled"] = not locked,
}
return actions
