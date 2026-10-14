-- Approve the pending plan so the controller applies it.
obj.spec.approvePlan = obj.status.plan.pending
return obj
