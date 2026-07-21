package controlplane

import "context"

type multiApplier struct {
	appliers []LiveApplier
}

func NewMultiApplier(appliers ...LiveApplier) LiveApplier {
	filtered := make([]LiveApplier, 0, len(appliers))
	for _, applier := range appliers {
		if applier == nil {
			continue
		}
		filtered = append(filtered, applier)
	}
	return multiApplier{appliers: filtered}
}

func (a multiApplier) Apply(ctx context.Context, change LiveChangeSet) error {
	for _, applier := range a.appliers {
		if err := applier.Apply(ctx, change); err != nil {
			return err
		}
	}
	return nil
}
