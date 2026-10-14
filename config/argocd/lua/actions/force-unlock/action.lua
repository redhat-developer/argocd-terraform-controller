-- Force-unlock the state held by the lock recorded in status.lock.pending.
-- Only use this when no other process is using the state.
if obj.spec.tfstate == nil then
  obj.spec.tfstate = {}
end
obj.spec.tfstate.forceUnlock = "auto"
return obj
