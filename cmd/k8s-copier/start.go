package main

import (
	"context"

	copier "github.com/michaelfig/k8s-copier/pkg/controller"
	ctrl "sigs.k8s.io/controller-runtime"
)

func Register(mgr ctrl.Manager) error {
	// TODO(mfig): Parse the resources supplied by --target= option.
	targets := []string{"federatedsecret"}
	namespaces := []string{"cloud"}

	ctx := context.TODO()
	c := copier.New(&ctx, mgr.GetConfig(), namespaces)

	for _, target := range targets {
		if err := c.AddTarget(target); err != nil {
			return err
		}
	}

	mgr.Add(c)
	return nil
}
