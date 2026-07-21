package controlplane

import "context"

type sourceStateApplierTarget interface {
	ApplySourceStates(map[string]bool)
}

type sourceStateApplier struct {
	target sourceStateApplierTarget
}

func NewSourceStateApplier(target sourceStateApplierTarget) LiveApplier {
	return sourceStateApplier{target: target}
}

func (a sourceStateApplier) Apply(ctx context.Context, change LiveChangeSet) error {
	_ = ctx
	if a.target == nil || change.EffectiveConfig == nil {
		return nil
	}
	a.target.ApplySourceStates(change.EffectiveConfig.Sources.Enabled)
	return nil
}
