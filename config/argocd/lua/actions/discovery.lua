-- Actions available on infra.argocd.io/Terraform.
local actions = {}
local spec = obj.spec or {}
local status = obj.status or {}

local pending = ""
if status.plan ~= nil and status.plan.pending ~= nil then
  pending = status.plan.pending
end
local approved = spec.force == true or spec.approvePlan == "auto" or (spec.approvePlan ~= nil and spec.approvePlan ~= "" and string.sub(pending, 1, string.len(spec.approvePlan)) == spec.approvePlan)
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
